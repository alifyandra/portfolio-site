package digest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/artifact"
	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
)

// The two-stage split of the digest (ADR 0014). digest.scrape fetches the active
// Sources on-box and persists one Artifact per source; digest.llm assembles those
// Artifacts into the document and summarizes it. Splitting them means the LLM stage
// can re-run over stored input without re-scraping, and can be claimed by an
// external runner. The monolithic digest.build (ADR 0013) is unchanged and remains
// the live path until a gated cutover.
const (
	// ArtifactInlineMaxBytes is the threshold under which an Artifact's payload is
	// stored inline in Postgres; larger payloads go to S3.
	ArtifactInlineMaxBytes = 256 * 1024
	// artifactTTL is how long a scrape Artifact lives before the sweeper may expire it.
	artifactTTL = 7 * 24 * time.Hour
	// scrapeLabelPrefix labels a scrape Artifact by its Source, e.g. "source:hn".
	scrapeLabelPrefix = "source:"
	// artifactS3Prefix is the assets-bucket key prefix for over-threshold Artifacts.
	artifactS3Prefix = "digest-artifacts/"
)

// artifactStore is the subset of *storage.Store the stages need. Small enough that
// a test can pass nil for the inline path (no S3), which is the common case.
type artifactStore interface {
	PutObject(ctx context.Context, key, contentType string, body []byte) error
	GetObject(ctx context.Context, key string) ([]byte, error)
}

// Scrape runs the digest.scrape stage: fetch each active Source and persist one
// pending Artifact per successful fetch, linked to the ScheduledJob (resolved from
// the producing JobRun) and to that run. It never calls Anthropic. It errors when
// every source fails (so the run is marked failed and retried); zero active sources
// is a clean no-op. now is the wall clock (drives expires_at); store may be nil when
// every payload fits inline. See ADR 0014.
func (b *Builder) Scrape(ctx context.Context, store artifactStore, jobRunID int, now time.Time) error {
	job, err := b.ent.JobRun.Query().Where(jobrun.IDEQ(jobRunID)).QueryJob().Only(ctx)
	if err != nil {
		return fmt.Errorf("digest: scrape: resolve job for run %d: %w", jobRunID, err)
	}

	sources, err := ActiveSourceInputs(ctx, b.ent)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		slog.Warn("digest: scrape: no active sources; nothing to scrape")
		return nil
	}

	expires := now.Add(artifactTTL)
	written, failed := 0, 0
	for _, s := range sources {
		content, ferr := b.content.fetcher.Fetch(ctx, s.URL)
		if ferr != nil {
			slog.Warn("digest: scrape: source fetch failed", "source", s.Name, "url", s.URL, "err", ferr)
			failed++
			continue
		}
		section := SourceSection(s.Name, s.Type, s.URL, content)
		if err := b.writeArtifact(ctx, store, job.ID, jobRunID, scrapeLabelPrefix+s.Name, section, expires); err != nil {
			// A write failure is infrastructural (DB/S3): fail the run so it retries,
			// rather than silently producing a partial scrape.
			return fmt.Errorf("digest: scrape: write artifact %q: %w", s.Name, err)
		}
		written++
	}
	if written == 0 {
		return fmt.Errorf("digest: scrape: all %d sources failed to fetch", len(sources))
	}
	slog.Info("digest: scraped", "written", written, "failed", failed, "sources", len(sources))
	return nil
}

// writeArtifact persists one scrape Artifact, inline when small and in S3 otherwise.
func (b *Builder) writeArtifact(ctx context.Context, store artifactStore, jobID, runID int, label, content string, expires time.Time) error {
	create := b.ent.Artifact.Create().
		SetLabel(label).
		SetContentType("text/markdown").
		SetSizeBytes(len(content)).
		SetStatus(artifact.StatusPending).
		SetExpiresAt(expires).
		SetJobID(jobID).
		SetProducedByID(runID)

	if len(content) <= ArtifactInlineMaxBytes {
		create = create.SetStorage(artifact.StorageInline).SetContent(content)
	} else {
		if store == nil {
			return fmt.Errorf("artifact %q is %d bytes (over the inline limit) but no object store is configured", label, len(content))
		}
		key := artifactS3Prefix + fmt.Sprintf("%d-%s.md", runID, sanitizeLabel(label))
		if err := store.PutObject(ctx, key, "text/markdown", []byte(content)); err != nil {
			return err
		}
		create = create.SetStorage(artifact.StorageS3).SetS3Key(key)
	}
	return create.Exec(ctx)
}

// AssembleFromArtifacts builds the digest document from the pending scrape Artifacts
// (status pending, label "source:*"), in stable id order, reading each payload inline
// or from S3. It returns the document and the ids of the artifacts that composed it
// (to be marked consumed once the LLM stage succeeds). Zero pending artifacts yields
// an empty doc and no ids. See ADR 0014.
func (b *Builder) AssembleFromArtifacts(ctx context.Context, store artifactStore) (doc string, ids []int, err error) {
	arts, err := b.ent.Artifact.Query().
		Where(
			artifact.StatusEQ(artifact.StatusPending),
			artifact.LabelHasPrefix(scrapeLabelPrefix),
		).
		Order(ent.Asc(artifact.FieldID)).
		All(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("digest: assemble: query artifacts: %w", err)
	}
	if len(arts) == 0 {
		return "", nil, nil
	}

	var sb strings.Builder
	for _, a := range arts {
		var content string
		switch a.Storage {
		case artifact.StorageInline:
			content = a.Content
		case artifact.StorageS3:
			if store == nil {
				return "", nil, fmt.Errorf("digest: assemble: artifact %d is in S3 but no object store is configured", a.ID)
			}
			data, gerr := store.GetObject(ctx, a.S3Key)
			if gerr != nil {
				return "", nil, fmt.Errorf("digest: assemble: read artifact %d from s3: %w", a.ID, gerr)
			}
			content = string(data)
		}
		sb.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			sb.WriteString("\n\n")
		}
		ids = append(ids, a.ID)
	}
	return sb.String(), ids, nil
}

// LlmLocal runs the digest.llm stage on-box synchronously (DIGEST_MODE=local): it
// assembles the pending scrape Artifacts, summarizes them in one blocking call,
// persists a completed date-keyed Digest, and marks those artifacts consumed by this
// run. Zero pending artifacts is a clean no-op ack. ErrNotConfigured (no Anthropic
// key) is returned for the caller to ack. The fargate path lives in the worker
// (it needs the Fargate launcher). See ADR 0014.
func (b *Builder) LlmLocal(ctx context.Context, store artifactStore, jobRunID int, date time.Time) error {
	if !b.content.Configured() {
		slog.Warn("digest: llm: anthropic not configured; skipping")
		return ErrNotConfigured
	}

	doc, ids, err := b.AssembleFromArtifacts(ctx, store)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		slog.Warn("digest: llm: no pending artifacts; nothing to summarize")
		return nil
	}

	result, buildErr := b.content.BuildAssembled(ctx, date, doc)
	if result != nil {
		if perr := Persist(ctx, b.ent, result); perr != nil {
			return perr
		}
	}
	if buildErr != nil {
		return buildErr
	}
	return b.MarkConsumed(ctx, ids, jobRunID)
}

// MarkConsumed marks the given artifacts done and attributes them to the consuming
// run. Exported so the worker's fargate digest.llm path can call it after a
// successful submit. A no-op for an empty id set.
func (b *Builder) MarkConsumed(ctx context.Context, ids []int, jobRunID int) error {
	if len(ids) == 0 {
		return nil
	}
	if err := b.ent.Artifact.Update().
		Where(artifact.IDIn(ids...)).
		SetStatus(artifact.StatusDone).
		SetConsumedByID(jobRunID).
		Exec(ctx); err != nil {
		return fmt.Errorf("digest: mark artifacts consumed: %w", err)
	}
	return nil
}

// Content exposes the ContentBuilder so callers outside the package (the worker's
// fargate digest.llm path) can submit an already-assembled document.
func (b *Builder) Content() *ContentBuilder { return b.content }

// sanitizeLabel makes a label safe for an S3 key (":" and spaces to "-").
func sanitizeLabel(label string) string {
	return strings.NewReplacer(":", "-", " ", "-", "/", "-").Replace(label)
}

// compile-time assertion that the concrete store satisfies the minimal interface.
var _ artifactStore = (*storage.Store)(nil)

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

	// Fetch every source first (network I/O, outside the transaction below so it stays
	// short). A per-source fetch failure is tolerated: one failed source still yields a
	// digest from the rest (ADR 0014). Only when every source fails is the run failed.
	expires := now.Add(artifactTTL)
	type section struct{ label, content string }
	sections := make([]section, 0, len(sources))
	failed := 0
	for _, s := range sources {
		content, ferr := b.content.fetcher.Fetch(ctx, s.URL)
		if ferr != nil {
			slog.Warn("digest: scrape: source fetch failed", "source", s.Name, "url", s.URL, "err", ferr)
			failed++
			continue
		}
		sections = append(sections, section{
			label:   scrapeLabelPrefix + s.Name,
			content: SourceSection(s.Name, s.Type, s.URL, content),
		})
	}
	if len(sections) == 0 {
		return fmt.Errorf("digest: scrape: all %d sources failed to fetch", len(sources))
	}

	// Persist atomically. A retry re-running this same run must not stack duplicate
	// artifacts, and a mid-loop write failure must not leave a partial scrape. Both are
	// covered by one transaction that first clears any still-pending source artifacts
	// this run produced before (idempotent per run+source), then writes the fresh set; a
	// write failure rolls the whole batch back and fails the run for the next tick.
	tx, err := b.ent.Tx(ctx)
	if err != nil {
		return fmt.Errorf("digest: scrape: begin tx: %w", err)
	}
	if _, err := tx.Artifact.Delete().
		Where(
			artifact.HasProducedByWith(jobrun.IDEQ(jobRunID)),
			artifact.LabelHasPrefix(scrapeLabelPrefix),
			artifact.StatusEQ(artifact.StatusPending),
		).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("digest: scrape: clear prior artifacts: %w", err)
	}
	for _, sec := range sections {
		if err := b.writeArtifact(ctx, tx.Artifact, store, job.ID, jobRunID, sec.label, sec.content, expires); err != nil {
			_ = tx.Rollback()
			// A write failure is infrastructural (DB/S3): fail the run so it retries,
			// rather than committing a partial scrape.
			return fmt.Errorf("digest: scrape: write artifact %q: %w", sec.label, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("digest: scrape: commit: %w", err)
	}
	slog.Info("digest: scraped", "written", len(sections), "failed", failed, "sources", len(sources))
	return nil
}

// writeArtifact persists one scrape Artifact through the given client (a *ent.Client
// artifact client or a transaction's), inline when small and in S3 otherwise. Taking
// the client explicitly lets Scrape write the whole set inside one transaction.
func (b *Builder) writeArtifact(ctx context.Context, artifacts *ent.ArtifactClient, store artifactStore, jobID, runID int, label, content string, expires time.Time) error {
	create := artifacts.Create().
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

// AssembleFromArtifacts builds the digest document from ONE scrape run's pending
// Artifacts (status pending, label "source:*"), in stable id order, reading each
// payload inline or from S3. It scopes to the NEWEST scrape run's artifacts: if a
// prior day's llm stage failed, that day's source artifacts stay pending, and folding
// every pending artifact globally would merge two days into one digest. The newest
// scrape run is the one that produced the highest-id still-pending source artifact (a
// later run's artifacts are all inserted after an earlier run's), found via the
// produced_by edge. Older stale pending artifacts are left for the sweeper. It returns
// the document and the ids of the artifacts that composed it (to be marked consumed
// once the LLM stage succeeds). Zero pending artifacts yields an empty doc and no ids.
// See ADR 0014.
func (b *Builder) AssembleFromArtifacts(ctx context.Context, store artifactStore) (doc string, ids []int, err error) {
	newest, err := b.ent.Artifact.Query().
		Where(
			artifact.StatusEQ(artifact.StatusPending),
			artifact.LabelHasPrefix(scrapeLabelPrefix),
		).
		Order(ent.Desc(artifact.FieldID)).
		WithProducedBy().
		First(ctx)
	if ent.IsNotFound(err) {
		return "", nil, nil // no pending scrape artifacts
	}
	if err != nil {
		return "", nil, fmt.Errorf("digest: assemble: find newest scrape run: %w", err)
	}
	if newest.Edges.ProducedBy == nil {
		return "", nil, fmt.Errorf("digest: assemble: artifact %d has no producing run", newest.ID)
	}
	runID := newest.Edges.ProducedBy.ID

	arts, err := b.ent.Artifact.Query().
		Where(
			artifact.StatusEQ(artifact.StatusPending),
			artifact.LabelHasPrefix(scrapeLabelPrefix),
			artifact.HasProducedByWith(jobrun.IDEQ(runID)),
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

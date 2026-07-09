// Command seed inserts starter Projects (idempotent by slug). Run once after
// the database is up: `make seed`. Replace/extend these with real content.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/alifyandra/portfolio-site/backend/ent/playlist"
	"github.com/alifyandra/portfolio-site/backend/ent/project"
	"github.com/alifyandra/portfolio-site/backend/ent/source"
	"github.com/alifyandra/portfolio-site/backend/internal/bootstrap"
	"github.com/alifyandra/portfolio-site/backend/internal/config"
)

type seedProject struct {
	slug, title, summary, description, repo, live string
	tags                                          []string
	featured                                      bool
	order                                         int
}

// seededPlaylistIDs are the hand-curated Spotify playlist IDs shown in the Music
// panel, in display order. Seeded idempotently into the Playlist table (by
// spotify_id) so the Admin Console can edit the set without a redeploy. These are
// the IDs that were previously hardcoded in the Spotify handler.
var seededPlaylistIDs = []string{
	"6PQYqQbW6AYd2QRiJImcJF",
	"3rE4pg8uhwsL1T0NhbLJnR",
	"3ZmZ7wHmzm2CvF9ZqyGuVs",
	"0dqCnKQCRLstotAISODoQO",
	"1fU0ZWngfA6A9t6Yh0uvCI",
	"13jonvKyZsTWcabIINLzWc",
}

// seededSources are the public, unauthenticated origins the digest.build Job
// reads from, seeded idempotently by url so local dev produces a real Digest. See
// CONTEXT.md ("Source") and ADR 0013.
type seedSource struct {
	name, url, typ string
}

var seededSources = []seedSource{
	{name: "Hacker News", url: "https://hnrss.org/frontpage", typ: "rss"},
	{name: "BBC News", url: "https://feeds.bbci.co.uk/news/rss.xml", typ: "rss"},
}

var seeds = []seedProject{
	{
		slug:        "openresim-analytics",
		title:       "Openresim Analytics SaaS",
		summary:     "Rebuilt a legacy analytics system into a scalable SaaS platform supporting AI-driven analytics.",
		description: "Led the end-to-end redevelopment using Django REST Framework and Next.js. Cut analysis computation time by ~80% via query optimisation and indexing, and added Celery/Redis task queues with real-time progress over websockets.",
		tags:        []string{"Django", "Next.js", "PostgreSQL", "Celery", "Redis", "DigitalOcean"},
		featured:    true,
		order:       1,
	},
	{
		slug:        "oy-payments",
		title:       "OY! Indonesia Payments",
		summary:     "Payment acceptance & e-wallet integrations serving hundreds of thousands of users.",
		description: "Built webhook-driven transaction services and an OVO e-wallet integration with Java Spring & Spark, plus RabbitMQ-based async processing and Twilio WhatsApp notifications.",
		tags:        []string{"Java", "Spring", "RabbitMQ", "Webhooks"},
		featured:    true,
		order:       2,
	},
	{
		slug:        "portfolio-site",
		title:       "This Portfolio Site",
		summary:     "Go + Ent + Huma backend, Next.js frontend, contract-first codegen, deployed on AWS.",
		description: "A from-scratch portfolio with a code-first OpenAPI pipeline (Huma -> orval), SQS async seam, and a single-box docker-compose deploy. See the repo's docs/adr for the decisions behind it.",
		tags:        []string{"Go", "Next.js", "AWS", "Docker", "OpenAPI"},
		repo:        "https://github.com/alifyandra/portfolio-site",
		featured:    true,
		order:       0,
	},
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	ctx := context.Background()
	app, err := bootstrap.New(ctx, cfg)
	if err != nil {
		slog.Error("bootstrap", "err", err)
		os.Exit(1)
	}
	defer app.Close()

	client := app.Deps.Ent
	for _, s := range seeds {
		exists, err := client.Project.Query().Where(project.SlugEQ(s.slug)).Exist(ctx)
		if err != nil {
			slog.Error("query", "err", err)
			os.Exit(1)
		}
		if exists {
			slog.Info("skip existing", "slug", s.slug)
			continue
		}
		_, err = client.Project.Create().
			SetSlug(s.slug).
			SetTitle(s.title).
			SetSummary(s.summary).
			SetDescription(s.description).
			SetTags(s.tags).
			SetRepoURL(s.repo).
			SetLiveURL(s.live).
			SetFeatured(s.featured).
			SetSortOrder(s.order).
			Save(ctx)
		if err != nil {
			slog.Error("create", "slug", s.slug, "err", err)
			os.Exit(1)
		}
		slog.Info("seeded", "slug", s.slug)
	}

	// Curated Spotify playlists, idempotent by spotify_id. Order follows the slice.
	for i, id := range seededPlaylistIDs {
		exists, err := client.Playlist.Query().Where(playlist.SpotifyIDEQ(id)).Exist(ctx)
		if err != nil {
			slog.Error("query playlist", "err", err)
			os.Exit(1)
		}
		if exists {
			slog.Info("skip existing playlist", "spotify_id", id)
			continue
		}
		if _, err := client.Playlist.Create().
			SetSpotifyID(id).
			SetSortOrder(i).
			Save(ctx); err != nil {
			slog.Error("create playlist", "spotify_id", id, "err", err)
			os.Exit(1)
		}
		slog.Info("seeded playlist", "spotify_id", id)
	}

	// Digest sources, idempotent by url.
	for _, s := range seededSources {
		exists, err := client.Source.Query().Where(source.URLEQ(s.url)).Exist(ctx)
		if err != nil {
			slog.Error("query source", "err", err)
			os.Exit(1)
		}
		if exists {
			slog.Info("skip existing source", "url", s.url)
			continue
		}
		if _, err := client.Source.Create().
			SetName(s.name).
			SetURL(s.url).
			SetType(source.Type(s.typ)).
			Save(ctx); err != nil {
			slog.Error("create source", "url", s.url, "err", err)
			os.Exit(1)
		}
		slog.Info("seeded source", "url", s.url)
	}

	slog.Info("seed complete")
}

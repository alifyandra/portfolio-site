// Package bootstrap wires together the application's dependencies from config.
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib" // register the "pgx" database/sql driver

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
	"github.com/alifyandra/portfolio-site/backend/internal/cache"
	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/digest"
	"github.com/alifyandra/portfolio-site/backend/internal/email"
	"github.com/alifyandra/portfolio-site/backend/internal/fargate"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
	"github.com/alifyandra/portfolio-site/backend/internal/server"
	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
	"github.com/alifyandra/portfolio-site/backend/internal/whatsapp"
)

// App holds the constructed dependencies and a cleanup function.
type App struct {
	Deps  *server.Deps
	close func() error
}

// Close releases all held resources.
func (a *App) Close() error { return a.close() }

// New constructs all dependencies. In development it also runs Ent's
// auto-migration so the schema is created on first run.
func New(ctx context.Context, cfg *config.Config) (*App, error) {
	// DATABASE_URL is required for anything that opens the DB (api, worker). It is
	// not a parse-time requirement (the digest Fargate task loads config but never
	// opens the DB, ADR 0013), so enforce it here where the connection is made.
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	// Open the *sql.DB with the pgx stdlib driver (registered as "pgx"), then
	// hand it to Ent with the Postgres dialect. ent.Open won't accept "pgx"
	// directly — it only maps the dialect names to database/sql driver names.
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	entClient := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))

	if cfg.AutoMigrate {
		if err := entClient.Schema.Create(ctx); err != nil {
			_ = entClient.Close()
			return nil, fmt.Errorf("running auto-migration: %w", err)
		}
	}

	redisClient, err := cache.New(ctx, cfg.RedisURL)
	if err != nil {
		_ = entClient.Close()
		return nil, err
	}

	store, err := storage.New(ctx, cfg)
	if err != nil {
		_ = entClient.Close()
		_ = redisClient.Close()
		return nil, err
	}

	q, err := queue.New(ctx, cfg)
	if err != nil {
		_ = entClient.Close()
		_ = redisClient.Close()
		return nil, err
	}

	sp := spotify.New(cfg.SpotifyClientID, cfg.SpotifyClientSecret, cfg.SpotifyRefreshToken)

	mailer, err := email.New(ctx, cfg)
	if err != nil {
		_ = entClient.Close()
		_ = redisClient.Close()
		return nil, err
	}

	authSvc := auth.New(entClient, auth.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.GoogleRedirectURL,
		AdminEmails:  cfg.AdminEmails,
		FriendEmails: cfg.FriendEmails,
		CookieDomain: cfg.SessionCookieDomain,
		CookieSecure: cfg.IsProduction(),
		FrontendURL:  cfg.FrontendURL,
	})

	// WhatsApp sidecar provider: a fixed URL (static) or a per-batch Fargate
	// launcher (fargate), selected by WA_SIDECAR_MODE. Fargate builds AWS clients;
	// static needs none. See ADR 11.
	waProvider, err := whatsapp.NewSidecarProvider(ctx, cfg)
	if err != nil {
		_ = entClient.Close()
		_ = redisClient.Close()
		return nil, err
	}

	// Digest builder: reads active Sources and summarizes them via the Anthropic
	// API. Cheap to construct (no network) and safe without an API key — it degrades
	// to a no-op. The worker runs it inline in local mode. See ADR 0013.
	digestBuilder := digest.NewBuilder(
		entClient,
		digest.NewAnthropic(cfg.AnthropicAPIKey, cfg.DigestModel, cfg.DigestMaxTokens),
		digest.NewFetcher(),
	)

	// Digest dispatch launcher: only the fargate mode builds an AWS client, so local
	// dev (DIGEST_MODE=local, the builder runs inline) needs no credentials. The
	// DIGEST_* identifiers are validated in config.Load when the mode is fargate.
	var digestLauncher *fargate.Launcher
	if cfg.DigestMode == "fargate" {
		digestLauncher, err = fargate.NewLauncher(ctx, fargate.Config{
			Region:          cfg.AWSRegion,
			Cluster:         cfg.DigestEcsCluster,
			TaskDefinition:  cfg.DigestTaskDefinition,
			ContainerName:   cfg.DigestContainerName,
			Subnets:         cfg.DigestSubnetIDs,
			SecurityGroupID: cfg.DigestSecurityGroupID,
			AssignPublicIP:  cfg.DigestAssignPublicIP,
		})
		if err != nil {
			_ = entClient.Close()
			_ = redisClient.Close()
			return nil, err
		}
	}

	deps := &server.Deps{
		Config:         cfg,
		Ent:            entClient,
		Redis:          redisClient,
		Spotify:        sp,
		Storage:        store,
		Queue:          q,
		Email:          mailer,
		Auth:           authSvc,
		WA:             waProvider,
		Digest:         digestBuilder,
		DigestLauncher: digestLauncher,
	}

	return &App{
		Deps: deps,
		close: func() error {
			_ = redisClient.Close()
			return entClient.Close()
		},
	}, nil
}

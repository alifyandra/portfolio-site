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
	"github.com/alifyandra/portfolio-site/backend/internal/email"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
	"github.com/alifyandra/portfolio-site/backend/internal/server"
	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
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
		CookieDomain: cfg.SessionCookieDomain,
		CookieSecure: cfg.IsProduction(),
		FrontendURL:  cfg.FrontendURL,
	})

	deps := &server.Deps{
		Config:  cfg,
		Ent:     entClient,
		Redis:   redisClient,
		Spotify: sp,
		Storage: store,
		Queue:   q,
		Email:   mailer,
		Auth:    authSvc,
	}

	return &App{
		Deps: deps,
		close: func() error {
			_ = redisClient.Close()
			return entClient.Close()
		},
	}, nil
}

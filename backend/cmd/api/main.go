// Command api runs the HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alifyandra/portfolio-site/backend/internal/api"
	"github.com/alifyandra/portfolio-site/backend/internal/bootstrap"
	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/server"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := bootstrap.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()

	handler, _ := server.New(app.Deps)

	// Keep Spotify data fresh in Redis on the backend's own schedule so the HTTP
	// handlers only ever read cache (no per-request Spotify calls). Stops with ctx.
	go api.NewSpotifyRefresher(app.Deps.Redis, app.Deps.Spotify, app.Deps.Ent).Run(ctx)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("api listening", "addr", srv.Addr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

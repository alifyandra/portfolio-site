// Command spec emits the OpenAPI document derived from the registered Huma
// operations. The output (openapi.yaml) is the single source of truth consumed
// by the frontend's orval codegen (see ADR 0005).
//
// Usage: go run ./cmd/spec > openapi.yaml
package main

import (
	"fmt"
	"os"

	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/server"
)

func main() {
	// Spec generation only needs route registration, not live connections, so we
	// pass a Deps with a minimal config and nil clients (handlers never run here).
	_, api := server.New(&server.Deps{
		Config: &config.Config{
			CORSAllowedOrigins: []string{"http://localhost:3000"},
		},
	})

	out, err := api.OpenAPI().YAML()
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to render OpenAPI:", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		os.Exit(1)
	}
}

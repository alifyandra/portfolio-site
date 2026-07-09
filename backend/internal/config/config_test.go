package config

import (
	"strings"
	"testing"
)

// setBase sets the always-required env so Load() gets past the unrelated required
// fields (DATABASE_URL) and we can exercise the WhatsApp sidecar-mode validation.
func setBase(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
}

// TestLoad_NoDatabaseURL guards the digest Fargate task path (ADR 0013, Shape B):
// cmd/digest calls Load() but never opens the DB, so a missing DATABASE_URL must
// not fail parsing. The requirement moved to bootstrap.New (the DB-open site).
func TestLoad_NoDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "") // empty (not required at parse), digest-task shape
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() without DATABASE_URL = %v, want nil (digest task loads config but never opens the DB)", err)
	}
	if cfg.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty", cfg.DatabaseURL)
	}
}

func TestLoadSidecarMode(t *testing.T) {
	t.Run("default mode is static and loads clean", func(t *testing.T) {
		setBase(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.WaSidecarMode != "static" {
			t.Errorf("WaSidecarMode = %q, want %q", cfg.WaSidecarMode, "static")
		}
	})

	t.Run("static URL without secret is rejected", func(t *testing.T) {
		setBase(t)
		t.Setenv("WA_SIDECAR_URL", "http://sidecar:8081")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "WA_SIDECAR_SECRET") {
			t.Fatalf("Load() error = %v, want it to name WA_SIDECAR_SECRET", err)
		}
	})

	t.Run("unknown mode is rejected", func(t *testing.T) {
		setBase(t)
		t.Setenv("WA_SIDECAR_MODE", "kubernetes")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "WA_SIDECAR_MODE") {
			t.Fatalf("Load() error = %v, want it to name WA_SIDECAR_MODE", err)
		}
	})

	// A fully-valid fargate config; each subtest below omits exactly one key and
	// asserts Load names the missing var.
	fargate := map[string]string{
		"WA_SIDECAR_SECRET":    "s3cr3t",
		"WA_ECS_CLUSTER":       "portfolio-wa",
		"WA_TASK_DEFINITION":   "portfolio-wa-sidecar",
		"WA_SUBNET_IDS":        "subnet-a,subnet-b",
		"WA_SECURITY_GROUP_ID": "sg-123",
	}

	for missing := range fargate {
		t.Run("fargate missing "+missing, func(t *testing.T) {
			setBase(t)
			t.Setenv("WA_SIDECAR_MODE", "fargate")
			for k, v := range fargate {
				if k == missing {
					continue // leave it genuinely absent
				}
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("Load() with %s absent: error = %v, want it to name %s", missing, err, missing)
			}
		})
	}

	t.Run("fargate with all identifiers loads clean", func(t *testing.T) {
		setBase(t)
		t.Setenv("WA_SIDECAR_MODE", "fargate")
		for k, v := range fargate {
			t.Setenv(k, v)
		}
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if got := len(cfg.WaSubnetIDs); got != 2 {
			t.Errorf("WaSubnetIDs = %v (len %d), want 2 entries", cfg.WaSubnetIDs, got)
		}
		if !cfg.WaAssignPublicIP {
			t.Errorf("WaAssignPublicIP = false, want true by default (public subnet, no NAT)")
		}
		if cfg.WaSidecarPort != 8081 {
			t.Errorf("WaSidecarPort = %d, want 8081 by default", cfg.WaSidecarPort)
		}
	})
}

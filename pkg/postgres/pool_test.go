package postgres

import (
	"testing"
	"time"
)

func TestBuildPoolConfigBoundsDefaultConnections(t *testing.T) {
	t.Setenv("PGPOOL_MAX_CONNS", "")

	cfg, err := buildPoolConfig("postgres://user:pass@127.0.0.1:5432/teamie_identity?sslmode=disable")
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}
	if cfg.MaxConns != DefaultMaxConns {
		t.Fatalf("MaxConns = %d, want %d", cfg.MaxConns, DefaultMaxConns)
	}
	if cfg.MinConns != 0 {
		t.Fatalf("MinConns = %d, want 0", cfg.MinConns)
	}
	if cfg.MaxConnIdleTime != time.Minute {
		t.Fatalf("MaxConnIdleTime = %v, want %v", cfg.MaxConnIdleTime, time.Minute)
	}
	if got := cfg.ConnConfig.RuntimeParams["timezone"]; got != "UTC" {
		t.Fatalf("timezone = %q, want UTC", got)
	}
}

func TestBuildPoolConfigHonorsEnvAndOptionOverrides(t *testing.T) {
	t.Setenv("PGPOOL_MAX_CONNS", "3")

	cfg, err := buildPoolConfig("postgres://user:pass@127.0.0.1:5432/teamie_chat?sslmode=disable")
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}
	if cfg.MaxConns != 3 {
		t.Fatalf("MaxConns = %d, want 3 from PGPOOL_MAX_CONNS", cfg.MaxConns)
	}

	cfgOpt, err := buildPoolConfig(
		"postgres://user:pass@127.0.0.1:5432/teamie_gateway?sslmode=disable",
		WithMaxConns(2),
		WithMinConns(5),
	)
	if err != nil {
		t.Fatalf("buildPoolConfig with opts: %v", err)
	}
	if cfgOpt.MaxConns != 2 {
		t.Fatalf("MaxConns = %d, want 2 from WithMaxConns", cfgOpt.MaxConns)
	}
	if cfgOpt.MinConns != 2 {
		t.Fatalf("MinConns = %d, want clamped to 2", cfgOpt.MinConns)
	}
}

func TestBuildPoolConfigPreservesExplicitDSNAndRejectsInvalidEnv(t *testing.T) {
	t.Setenv("PGPOOL_MAX_CONNS", "")

	cfg, err := buildPoolConfig("postgres://user:pass@127.0.0.1:5432/teamie_identity?pool_max_conns=5")
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}
	if cfg.MaxConns != 5 {
		t.Fatalf("MaxConns = %d, want explicit URL pool_max_conns=5", cfg.MaxConns)
	}

	t.Setenv("PGPOOL_MAX_CONNS", "0")
	if _, err := buildPoolConfig("postgres://user:pass@127.0.0.1:5432/teamie_identity"); err == nil {
		t.Fatal("expected error for invalid PGPOOL_MAX_CONNS=0")
	}
}

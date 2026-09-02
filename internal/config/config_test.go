package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFollowDefaults(t *testing.T) {
	cfg, err := LoadFollow(nil)
	if err != nil {
		t.Fatalf("LoadFollow: %v", err)
	}
	if cfg.RPCURL == "" || cfg.ReorgDepth != 1024 || time.Duration(cfg.PollInterval) != 15*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadFollowFileThenEnvThenFlagPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
follow:
  rpc_url: https://file.example/rpc
  reorg_depth: 64
  poll_interval: 5s
`)

	cfg, err := LoadFollow([]string{"-config", path})
	if err != nil {
		t.Fatalf("LoadFollow: %v", err)
	}
	if cfg.RPCURL != "https://file.example/rpc" || cfg.ReorgDepth != 64 {
		t.Fatalf("file values not applied: %+v", cfg)
	}

	t.Setenv("QI_RPC_URL", "https://env.example/rpc")
	cfg, err = LoadFollow([]string{"-config", path})
	if err != nil {
		t.Fatalf("LoadFollow: %v", err)
	}
	if cfg.RPCURL != "https://env.example/rpc" {
		t.Fatalf("env override not applied: %+v", cfg)
	}
	if cfg.ReorgDepth != 64 {
		t.Fatalf("file value should survive env override of a different field: %+v", cfg)
	}

	cfg, err = LoadFollow([]string{"-config", path, "-rpc", "https://flag.example/rpc"})
	if err != nil {
		t.Fatalf("LoadFollow: %v", err)
	}
	if cfg.RPCURL != "https://flag.example/rpc" {
		t.Fatalf("flag should win over file and env: %+v", cfg)
	}
}

func TestLoadFollowRejectsInvalid(t *testing.T) {
	if _, err := LoadFollow([]string{"-rpc", ""}); err == nil {
		t.Fatal("expected error for empty rpc_url")
	}
	if _, err := LoadFollow([]string{"-depth", "0"}); err == nil {
		t.Fatal("expected error for non-positive reorg_depth")
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

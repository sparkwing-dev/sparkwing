package main

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/profile"
)

// profileFlowPath redirects profile.DefaultPath() at a tempdir so the
// CLI helpers write their fixture to an isolated location instead of
// stomping the developer's real profiles.yaml.
func profileFlowPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.yaml")
	t.Setenv("SPARKWING_PROFILES", path)
	return path
}

func TestProfilesAdd_StoresDefaultRunner(t *testing.T) {
	path := profileFlowPath(t)
	if err := runProfilesAdd([]string{
		"--name", "prod",
		"--controller", "https://api.example.dev",
		"--default-runner", "cloud-gpu",
	}); err != nil {
		t.Fatalf("runProfilesAdd: %v", err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Profiles["prod"]
	if p == nil {
		t.Fatalf("prod profile missing; cfg=%+v", cfg)
	}
	if p.DefaultRunner != "cloud-gpu" {
		t.Errorf("DefaultRunner = %q, want cloud-gpu", p.DefaultRunner)
	}
	if got := p.EffectiveDefaultRunner(); got != "cloud-gpu" {
		t.Errorf("EffectiveDefaultRunner = %q, want cloud-gpu", got)
	}
}

func TestProfilesAdd_DefaultRunnerUnsetLeavesFieldEmpty(t *testing.T) {
	path := profileFlowPath(t)
	if err := runProfilesAdd([]string{
		"--name", "local",
		"--controller", "http://127.0.0.1:4344",
	}); err != nil {
		t.Fatalf("runProfilesAdd: %v", err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Profiles["local"]
	if p.DefaultRunner != "" {
		t.Errorf("DefaultRunner = %q, want empty (omit flag)", p.DefaultRunner)
	}
	if got := p.EffectiveDefaultRunner(); got != "local" {
		t.Errorf("EffectiveDefaultRunner = %q, want local (fallback)", got)
	}
}

func TestProfilesSet_UpdatesDefaultRunner(t *testing.T) {
	path := profileFlowPath(t)
	if err := runProfilesAdd([]string{
		"--name", "prod",
		"--controller", "https://api.example.dev",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := runProfilesSet([]string{
		"--name", "prod",
		"--default-runner", "cloud-linux",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Profiles["prod"].DefaultRunner; got != "cloud-linux" {
		t.Errorf("DefaultRunner after set = %q, want cloud-linux", got)
	}
}

func TestProfilesSet_DefaultRunnerEmptyClears(t *testing.T) {
	path := profileFlowPath(t)
	if err := runProfilesAdd([]string{
		"--name", "prod",
		"--controller", "https://api.example.dev",
		"--default-runner", "cloud-gpu",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := runProfilesSet([]string{
		"--name", "prod",
		"--default-runner", "",
	}); err != nil {
		t.Fatalf("set clear: %v", err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Profiles["prod"]
	if p.DefaultRunner != "" {
		t.Errorf("DefaultRunner after clear = %q, want empty", p.DefaultRunner)
	}
	if got := p.EffectiveDefaultRunner(); got != "local" {
		t.Errorf("EffectiveDefaultRunner after clear = %q, want local", got)
	}
}

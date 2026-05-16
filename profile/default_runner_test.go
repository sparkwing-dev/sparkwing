package profile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/profile"
)

func TestEffectiveDefaultRunner_NilProfile(t *testing.T) {
	var p *profile.Profile
	if got := p.EffectiveDefaultRunner(); got != "local" {
		t.Errorf("nil profile EffectiveDefaultRunner = %q, want local", got)
	}
}

func TestEffectiveDefaultRunner_EmptyField(t *testing.T) {
	p := &profile.Profile{}
	if got := p.EffectiveDefaultRunner(); got != "local" {
		t.Errorf("empty-field EffectiveDefaultRunner = %q, want local", got)
	}
}

func TestEffectiveDefaultRunner_DeclaredValue(t *testing.T) {
	p := &profile.Profile{DefaultRunner: "cloud-gpu"}
	if got := p.EffectiveDefaultRunner(); got != "cloud-gpu" {
		t.Errorf("EffectiveDefaultRunner = %q, want cloud-gpu", got)
	}
}

func TestSaveLoad_DefaultRunnerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	in := &profile.Config{
		Default: "prod",
		Profiles: map[string]*profile.Profile{
			"prod": {
				Controller:    "https://api.example.dev",
				DefaultRunner: "cloud-linux",
			},
		},
	}
	if err := profile.Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := profile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := out.Profiles["prod"]
	if got.DefaultRunner != "cloud-linux" {
		t.Errorf("DefaultRunner round-trip = %q, want cloud-linux", got.DefaultRunner)
	}
}

func TestSave_OmitsDefaultRunnerWhenUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	in := &profile.Config{
		Profiles: map[string]*profile.Profile{
			"local": {Controller: "http://127.0.0.1:4344"},
		},
	}
	if err := profile.Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), "default_runner") {
		t.Errorf("yaml should omit default_runner when unset:\n%s", raw)
	}
}

func TestSave_RoundTripsAfterLoad_PreservesDefaultRunner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(`profiles:
  prod:
    controller: https://api.example.dev
    default_runner: cloud-gpu
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profiles["prod"].DefaultRunner != "cloud-gpu" {
		t.Fatalf("DefaultRunner parsed = %q", cfg.Profiles["prod"].DefaultRunner)
	}
	// Save back and verify the field is still there.
	if err := profile.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "default_runner: cloud-gpu") {
		t.Errorf("expected default_runner: cloud-gpu in output:\n%s", raw)
	}
}

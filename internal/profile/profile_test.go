package profile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := profile.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if cfg == nil || cfg.Profiles == nil {
		t.Fatalf("expected non-nil cfg with empty Profiles map; got %+v", cfg)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	mirror := false
	cfg := &profile.Config{
		Profiles: map[string]*profile.Profile{
			"prod": {
				Controller:  &profile.ControllerSpec{URL: "https://api.example.dev", Token: "swu_x"},
				State:       &backends.Spec{Type: backends.TypeSQLite, Path: "/var/state.db"},
				MirrorLocal: &mirror,
			},
		},
	}
	if err := profile.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := profile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	prod := out.Profiles["prod"]
	if prod == nil {
		t.Fatal("prod profile missing on reload")
	}
	if prod.ControllerURL() != "https://api.example.dev" || prod.ControllerToken() != "swu_x" {
		t.Errorf("controller/token: %+v", prod)
	}
	if prod.State == nil || prod.State.Type != backends.TypeSQLite {
		t.Errorf("state: %+v", prod.State)
	}
	if prod.MirrorLocal == nil || *prod.MirrorLocal != false {
		t.Errorf("mirror_local: %+v", prod.MirrorLocal)
	}
}

func TestSave_0600Mode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := profile.Save(path, &profile.Config{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 (file carries tokens)", info.Mode().Perm())
	}
}

func TestNames_Sorted(t *testing.T) {
	cfg := &profile.Config{
		Profiles: map[string]*profile.Profile{
			"zebra": {}, "alpha": {}, "mango": {},
		},
	}
	got := cfg.Names()
	want := []string{"alpha", "mango", "zebra"}
	for i, n := range want {
		if got[i] != n {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], n)
		}
	}
}

func TestDefaultPath_RespectsEnv(t *testing.T) {
	t.Setenv("SPARKWING_PROFILES", "/tmp/custom.yaml")
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := profile.DefaultPath()
	if err != nil || got != "/tmp/custom.yaml" {
		t.Errorf("got (%q, %v), want /tmp/custom.yaml", got, err)
	}
}

func TestDefaultPath_XDG(t *testing.T) {
	t.Setenv("SPARKWING_PROFILES", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got, err := profile.DefaultPath()
	if err != nil || got != "/tmp/xdg/sparkwing/profiles.yaml" {
		t.Errorf("got (%q, %v), want /tmp/xdg/sparkwing/profiles.yaml", got, err)
	}
}

func TestEffectiveMirrorLocal_DefaultsTrue(t *testing.T) {
	if !(*profile.Profile)(nil).EffectiveMirrorLocal() {
		t.Error("nil profile should report MirrorLocal=true (laptop default)")
	}
	p := &profile.Profile{}
	if !p.EffectiveMirrorLocal() {
		t.Error("unset MirrorLocal should default to true")
	}
	f := false
	p.MirrorLocal = &f
	if p.EffectiveMirrorLocal() {
		t.Error("MirrorLocal=false should report false")
	}
}

func TestSurfaces_NilSafe(t *testing.T) {
	got := (*profile.Profile)(nil).Surfaces()
	if got.State != nil || got.Cache != nil || got.Logs != nil {
		t.Errorf("nil profile should yield zero Surfaces; got %+v", got)
	}
}

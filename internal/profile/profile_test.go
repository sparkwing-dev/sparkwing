package profile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

// safety: Save refuses a profiles path outside the sparkwing home unless the
// operator named the file, and a test binary's home is the test sandbox.
func savedAt(t *testing.T, path string) string {
	t.Helper()
	t.Setenv("SPARKWING_CONFIG", path)
	return path
}

func TestInheritControllerDefaults(t *testing.T) {
	prefilledTokenEnv := "PREFILLED"
	p := &profile.Profile{
		Controller: &profile.ControllerSpec{URL: "https://ctrl.example", Token: "tok-from-ctrl"},
		Secrets:    &backends.Spec{Type: backends.TypeController},
		State:      &backends.Spec{Type: backends.TypeController, URL: "https://state.override", Token: "tok-state"},
		Cache:      &backends.Spec{Type: backends.TypeController, TokenEnv: prefilledTokenEnv},
		Logs:       &backends.Spec{Type: backends.TypeSQLite, Path: "/var/sw.db"},
	}
	p.InheritControllerDefaults()
	if p.Secrets.URL != "https://ctrl.example" || p.Secrets.Token != "tok-from-ctrl" {
		t.Errorf("Secrets not filled from controller: %+v", p.Secrets)
	}
	if p.State.URL != "https://state.override" || p.State.Token != "tok-state" {
		t.Errorf("State (explicit) was overwritten: %+v", p.State)
	}
	if p.Cache.URL != "https://ctrl.example" || p.Cache.TokenEnv != prefilledTokenEnv || p.Cache.Token != "" {
		t.Errorf("Cache: URL should fill but Token must stay empty when TokenEnv is set: %+v", p.Cache)
	}
	if p.Logs.URL != "/var/sw.db"[:0]+"" || p.Logs.Path != "/var/sw.db" {
		t.Errorf("Logs (non-controller type) must not be touched: %+v", p.Logs)
	}
	explicit := &profile.Profile{
		Controller: &profile.ControllerSpec{URL: "https://ctrl.example"},
		Logs:       &backends.Spec{Type: backends.TypeController, URL: "https://ctrl.example"},
	}
	explicit.InheritControllerDefaults()
	if got := explicit.ExplicitLogsURL(); got != "https://ctrl.example" {
		t.Errorf("explicit colocated logs URL = %q", got)
	}
}

func TestSaveKeepsInheritedLogsURLDiscoverable(t *testing.T) {
	path := savedAt(t, filepath.Join(t.TempDir(), "config.yaml"))
	if err := fssecure.WriteFile(path, []byte("profiles:\n  prod:\n    controller: {url: https://ctrl.example}\n    logs: {type: controller}\n")); err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	prod := reloaded.Profiles["prod"]
	if prod.Logs.URL != prod.ControllerURL() || prod.ExplicitLogsURL() != "" {
		t.Fatalf("saved inherited logs URL became explicit: %+v", prod.Logs)
	}
}

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
	path := savedAt(t, filepath.Join(t.TempDir(), "config.yaml"))
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
	path := savedAt(t, filepath.Join(t.TempDir(), "config.yaml"))
	if err := profile.Save(path, &profile.Config{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private file mode = %v, %v; want 0600", info, err)
		}
	}
	file, err := fssecure.OpenPrivateConfig(path)
	if err != nil {
		t.Fatalf("saved config is not private: %v", err)
	}
	_ = file.Close()
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
	t.Setenv("SPARKWING_CONFIG", "/tmp/custom.yaml")
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := profile.DefaultPath()
	if err != nil || got != "/tmp/custom.yaml" {
		t.Errorf("got (%q, %v), want /tmp/custom.yaml", got, err)
	}
}

func TestDefaultPath_XDG(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got, err := profile.DefaultPath()
	if err != nil || got != filepath.Join("/tmp/xdg", "sparkwing", "config.yaml") {
		t.Errorf("got (%q, %v), want /tmp/xdg/sparkwing/config.yaml", got, err)
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

func TestSaveRewritesOnlyTheProfilesSection(t *testing.T) {
	path := savedAt(t, filepath.Join(t.TempDir(), "config.yaml"))
	before := "# this machine\nadmission:\n  budget: 50%,8gb # leave the desktop room\nprofiles:\n  old: {}\n"
	if err := fssecure.WriteFile(path, []byte(before)); err != nil {
		t.Fatal(err)
	}
	cfg := &profile.Config{Profiles: map[string]*profile.Profile{"prod": {Controller: &profile.ControllerSpec{URL: "https://ctrl.example"}}}}
	if err := profile.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# this machine", "budget: 50%,8gb # leave the desktop room", "prod:", "url: https://ctrl.example"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("config.yaml after Save lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), "old:") {
		t.Errorf("Save kept a profile the new set dropped:\n%s", body)
	}
}

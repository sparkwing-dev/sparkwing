package profile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

// safety: save refuses a profiles path outside the sparkwing home unless the
// operator named the file, and a test binary's home is the test sandbox.
func savedAt(t *testing.T, path string) string {
	t.Helper()
	t.Setenv("SPARKWING_CONFIG", path)
	return path
}

func TestInheritControllerDefaults(t *testing.T) {
	prefilledTokenEnv := "PREFILLED"
	p := &profile{
		Controller: &controllerSpec{URL: "https://ctrl.example", Token: "tok-from-ctrl"},
		Secrets:    &backends.Spec{Type: backends.TypeController},
		State:      &backends.Spec{Type: backends.TypeController, URL: "https://state.override", Token: "tok-state"},
	}
	p.inheritControllerDefaults()
	if p.Secrets.URL != "https://ctrl.example" || p.Secrets.Token != "tok-from-ctrl" {
		t.Errorf("Secrets not filled from controller: %+v", p.Secrets)
	}
	if p.State.URL != "https://state.override" || p.State.Token != "tok-state" {
		t.Errorf("State (explicit) was overwritten: %+v", p.State)
	}

	envSecrets := &profile{
		Controller: &controllerSpec{URL: "https://ctrl.example", Token: "tok-from-ctrl"},
		Secrets:    &backends.Spec{Type: backends.TypeController, TokenEnv: prefilledTokenEnv},
		State:      &backends.Spec{Type: backends.TypeSQLite, Path: "/var/sw.db"},
	}
	envSecrets.inheritControllerDefaults()
	if envSecrets.Secrets.URL != "https://ctrl.example" || envSecrets.Secrets.Token != "" {
		t.Errorf("Secrets: URL should fill but Token must stay empty when TokenEnv is set: %+v", envSecrets.Secrets)
	}
	if envSecrets.State.URL != "" || envSecrets.State.Path != "/var/sw.db" {
		t.Errorf("State (non-controller type) must not be touched: %+v", envSecrets.State)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if cfg == nil || cfg.Profiles == nil {
		t.Fatalf("expected non-nil cfg with empty Profiles map; got %+v", cfg)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := savedAt(t, filepath.Join(t.TempDir(), "config.yaml"))
	cfg := &config{
		Profiles: map[string]*profile{
			"prod": {
				Controller: &controllerSpec{URL: "https://api.example.dev", Token: "swu_x"},
			},
		},
	}
	if err := save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	prod := out.Profiles["prod"]
	if prod == nil {
		t.Fatal("prod profile missing on reload")
	}
	if prod.controllerURL() != "https://api.example.dev" || prod.controllerToken() != "swu_x" {
		t.Errorf("controller/token: %+v", prod)
	}
}

func TestSave_0600Mode(t *testing.T) {
	path := savedAt(t, filepath.Join(t.TempDir(), "config.yaml"))
	if err := save(path, &config{}); err != nil {
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
	cfg := &config{
		Profiles: map[string]*profile{
			"zebra": {}, "alpha": {}, "mango": {},
		},
	}
	got := cfg.names()
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
	got, err := defaultPath()
	if err != nil || got != "/tmp/custom.yaml" {
		t.Errorf("got (%q, %v), want /tmp/custom.yaml", got, err)
	}
}

func TestDefaultPath_XDG(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", "")
	if err := os.Unsetenv("SPARKWING_CONFIG"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got, err := defaultPath()
	if err != nil || got != filepath.Join("/tmp/xdg", "sparkwing", "config.yaml") {
		t.Errorf("got (%q, %v), want /tmp/xdg/sparkwing/config.yaml", got, err)
	}
}

func TestLoad_RefusesARemovedStateProfile(t *testing.T) {
	for name, body := range map[string]string{
		"s3":       "profiles:\n  shared:\n    state: {type: s3, bucket: b, prefix: state}\n",
		"postgres": "profiles:\n  shared:\n    state: {type: postgres, url: postgres://db/sparkwing}\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := savedAt(t, filepath.Join(t.TempDir(), "config.yaml"))
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := load(path)
			if err == nil {
				t.Fatal("Load accepted a removed state profile")
			}
			for _, want := range []string{`profile "shared"`, "is removed", backends.MigrationGuideURL} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should mention %q", err, want)
				}
			}
		})
	}
}

func TestLoad_RefusesRemovedKeys(t *testing.T) {
	for key, body := range map[string]string{
		"mirror_local": "profiles:\n  prod:\n    mirror_local: false\n",
		"logs":         "profiles:\n  prod:\n    logs: {type: controller}\n",
		"cache":        "profiles:\n  prod:\n    cache: {type: filesystem, path: /tmp/c}\n",
		"state":        "profiles:\n  prod:\n    state: {type: sqlite, batch_bytes: 1024}\n",
	} {
		path := savedAt(t, filepath.Join(t.TempDir(), "config.yaml"))
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := load(path); err == nil || !strings.Contains(err.Error(), "profiles.prod."+key) {
			t.Errorf("Load = %v, want %s refused as an unknown field", err, key)
		}
	}
}

func TestSurfaces_NilSafe(t *testing.T) {
	got := (*profile)(nil).surfaces()
	if got.State != nil || got.Secrets != nil {
		t.Errorf("nil profile should yield zero Surfaces; got %+v", got)
	}
}

func TestSaveRewritesOnlyTheProfilesSection(t *testing.T) {
	path := savedAt(t, filepath.Join(t.TempDir(), "config.yaml"))
	before := "# this machine\nadmission:\n  budget: 50%,8gb # leave the desktop room\nprofiles:\n  old: {controller: {url: https://old.example}}\n"
	if err := fssecure.WriteFile(path, []byte(before)); err != nil {
		t.Fatal(err)
	}
	cfg := &config{Profiles: map[string]*profile{"prod": {Controller: &controllerSpec{URL: "https://ctrl.example"}}}}
	if err := save(path, cfg); err != nil {
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

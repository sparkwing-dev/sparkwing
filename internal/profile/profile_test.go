package profile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := profile.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	// A missing file yields no user profiles, but the built-ins are
	// always materialized so detect rules work day-zero.
	for _, name := range []string{"gha", "kubernetes"} {
		if _, ok := cfg.Profiles[name]; !ok {
			t.Fatalf("built-in %q missing from empty config: %v", name, cfg.Names())
		}
	}
	if len(cfg.Profiles) != 2 {
		t.Fatalf("expected only the 2 built-ins, got %d: %v", len(cfg.Profiles), cfg.Names())
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	in := &profile.Config{
		Default: "local",
		Profiles: map[string]*profile.Profile{
			"local": {Controller: "http://127.0.0.1:4344"},
			"prod": {
				Controller:    "https://api.example.dev",
				Token:         "swu_test",
				Gitcache:      "https://gitcache.example.com",
				LogStore:      "s3://your-team-sparkwing-store/logs",
				ArtifactStore: "s3://your-team-sparkwing-store/cache",
			},
		},
	}
	if err := profile.Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Default != "local" {
		t.Fatalf("default: %q", out.Default)
	}
	if out.Profiles["local"].Controller != "http://127.0.0.1:4344" {
		t.Fatalf("local profile roundtrip: %+v", out.Profiles["local"])
	}
	if out.Profiles["prod"].Token != "swu_test" {
		t.Fatalf("prod token roundtrip: %+v", out.Profiles["prod"])
	}
	if out.Profiles["prod"].LogStore != "s3://your-team-sparkwing-store/logs" ||
		out.Profiles["prod"].ArtifactStore != "s3://your-team-sparkwing-store/cache" {
		t.Fatalf("storage URL roundtrip: %+v", out.Profiles["prod"])
	}
	if out.Profiles["local"].Name != "local" {
		t.Fatalf("Name not stamped on load: %+v", out.Profiles["local"])
	}
}

func TestSave_0600Mode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := profile.Save(path, &profile.Config{
		Profiles: map[string]*profile.Profile{
			"local": {Controller: "http://x"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode: got %o, want 0600", mode)
	}
}

func TestNames_Sorted(t *testing.T) {
	cfg := &profile.Config{
		Profiles: map[string]*profile.Profile{
			"zulu":  {},
			"alpha": {},
			"mike":  {},
		},
	}
	names := cfg.Names()
	if got := strings.Join(names, ","); got != "alpha,mike,zulu" {
		t.Fatalf("got %q, want alpha,mike,zulu", got)
	}
}

func TestDefaultPath_RespectsEnv(t *testing.T) {
	t.Setenv("SPARKWING_PROFILES", "/explicit/path.yaml")
	p, err := profile.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != "/explicit/path.yaml" {
		t.Fatalf("got %q", p)
	}
}

func TestDefaultPath_XDG(t *testing.T) {
	t.Setenv("SPARKWING_PROFILES", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	p, err := profile.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/xdg/sparkwing/profiles.yaml" {
		t.Fatalf("got %q", p)
	}
}

func TestSurfaces_FullTripleRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(`
default: shared-team
profiles:
  shared-team:
    state: { type: s3, bucket: team, prefix: state }
    cache: { type: s3, bucket: team, prefix: cache }
    logs: { type: s3, bucket: team, prefix: logs }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles["shared-team"]
	if p == nil {
		t.Fatalf("shared-team missing: %v", cfg.Names())
	}
	s := p.Surfaces()
	if s.State == nil || s.State.Type != backends.TypeS3 || s.State.Bucket != "team" || s.State.Prefix != "state" {
		t.Fatalf("state surface: %+v", s.State)
	}
	if s.Cache == nil || s.Cache.Prefix != "cache" {
		t.Fatalf("cache surface: %+v", s.Cache)
	}
	if s.Logs == nil || s.Logs.Prefix != "logs" {
		t.Fatalf("logs surface: %+v", s.Logs)
	}
}

func TestSurfaces_NilAndEmpty(t *testing.T) {
	var nilProfile *profile.Profile
	if s := nilProfile.Surfaces(); s.State != nil || s.Cache != nil || s.Logs != nil {
		t.Fatalf("nil profile should yield zero Surfaces, got %+v", s)
	}
	empty := &profile.Profile{Name: "bare"}
	if s := empty.Surfaces(); s.State != nil || s.Cache != nil || s.Logs != nil {
		t.Fatalf("profile with no specs should yield zero Surfaces, got %+v", s)
	}
}

func TestDetect_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(`
profiles:
  ci:
    detect: { env_var: MY_CI, equals: "yes" }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Profiles["ci"].Detect
	if d == nil || d.EnvVar != "MY_CI" || d.Equals != "yes" {
		t.Fatalf("detect round-trip: %+v", d)
	}
}

func TestEffectiveMirrorLocal_DefaultsTrue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(`
profiles:
  mirror-off:
    mirror_local: false
  mirror-default: {}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles["mirror-off"].MirrorLocal == nil || *cfg.Profiles["mirror-off"].MirrorLocal {
		t.Fatalf("mirror_local: false should deserialize to *bool=false: %+v", cfg.Profiles["mirror-off"].MirrorLocal)
	}
	if cfg.Profiles["mirror-off"].EffectiveMirrorLocal() {
		t.Fatal("mirror-off should report EffectiveMirrorLocal() == false")
	}
	if !cfg.Profiles["mirror-default"].EffectiveMirrorLocal() {
		t.Fatal("absent mirror_local should report EffectiveMirrorLocal() == true")
	}
	var nilProfile *profile.Profile
	if !nilProfile.EffectiveMirrorLocal() {
		t.Fatal("nil profile should report EffectiveMirrorLocal() == true")
	}
}

func TestMirrorLocal_FalseSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	no := false
	if err := profile.Save(path, &profile.Config{
		Profiles: map[string]*profile.Profile{
			"worker": {MirrorLocal: &no},
		},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Profiles["worker"]
	if got == nil || got.MirrorLocal == nil || *got.MirrorLocal {
		t.Fatalf("mirror_local: false should survive Save+Load: %+v", got)
	}
}

func TestBuiltinProfiles_DetectPredicates(t *testing.T) {
	b := profile.BuiltinProfiles()
	gha := b["gha"]
	if gha == nil || gha.Detect == nil || gha.Detect.EnvVar != "GITHUB_ACTIONS" || gha.Detect.Equals != "true" {
		t.Fatalf("gha detect predicate: %+v", gha)
	}
	k8s := b["kubernetes"]
	if k8s == nil || k8s.Detect == nil || k8s.Detect.EnvVar != "KUBERNETES_SERVICE_HOST" || !k8s.Detect.Present {
		t.Fatalf("kubernetes detect predicate: %+v", k8s)
	}
}

func TestLoad_MaterializesBuiltins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(`
profiles:
  prod: { controller: https://api.example.dev }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gha", "kubernetes"} {
		if _, ok := cfg.Profiles[name]; !ok {
			t.Fatalf("built-in %q not materialized: %v", name, cfg.Names())
		}
	}
}

// TestLoad_UserProfileOverridesBuiltinPerField mirrors the shape of the
// pkg/backends repo-wins-per-field merge test: a user-declared profile
// sharing a built-in's name keeps its own fields and inherits the
// built-in's detect block for the fields it leaves blank.
func TestLoad_UserProfileOverridesBuiltinPerField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(`
profiles:
  gha:
    state: { type: s3, bucket: team-ci, prefix: state }
  kubernetes:
    detect: { env_var: CUSTOM_K8S, present: true }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	gha := cfg.Profiles["gha"]
	if gha.State == nil || gha.State.Bucket != "team-ci" {
		t.Fatalf("user gha state should win: %+v", gha.State)
	}
	if gha.Detect == nil || gha.Detect.EnvVar != "GITHUB_ACTIONS" || gha.Detect.Equals != "true" {
		t.Fatalf("user gha should inherit built-in detect: %+v", gha.Detect)
	}
	k8s := cfg.Profiles["kubernetes"]
	if k8s.Detect == nil || k8s.Detect.EnvVar != "CUSTOM_K8S" || !k8s.Detect.Present {
		t.Fatalf("user kubernetes detect should override built-in: %+v", k8s.Detect)
	}
}

func TestSave_OmitsUnmodifiedBuiltins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	loaded, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Profiles["prod"] = &profile.Profile{Controller: "https://api.example.dev"}
	if err := profile.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "gha") || strings.Contains(string(raw), "kubernetes") {
		t.Fatalf("unmodified built-ins should not persist to disk:\n%s", raw)
	}
}

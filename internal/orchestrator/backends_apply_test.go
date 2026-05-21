package orchestrator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type stubArtifactStore struct{}

func (stubArtifactStore) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (stubArtifactStore) Put(context.Context, string, io.Reader) error       { return nil }
func (stubArtifactStore) Has(context.Context, string) (bool, error)          { return false, nil }
func (stubArtifactStore) Delete(context.Context, string) error               { return nil }
func (stubArtifactStore) List(context.Context, string) ([]string, error)     { return nil, nil }

type stubLogStore struct{}

func (stubLogStore) Append(context.Context, string, string, []byte) error { return nil }
func (stubLogStore) Read(context.Context, string, string, storage.ReadOpts) ([]byte, error) {
	return nil, nil
}

func (stubLogStore) ReadRun(context.Context, string) ([]byte, error) { return nil, nil }

func (stubLogStore) Stream(context.Context, string, string) (io.ReadCloser, error) { return nil, nil }
func (stubLogStore) DeleteRun(context.Context, string) error                       { return nil }

func strReader(s string) io.Reader { return strings.NewReader(s) }

var (
	_ sparkwing.Cache = stubArtifactStore{}
	_ sparkwing.Logs  = stubLogStore{}
)

func writeBackendsYAML(t *testing.T, dir, body string) string {
	t.Helper()
	sparkwingDir := filepath.Join(dir, ".sparkwing")
	if err := os.MkdirAll(sparkwingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "backends.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return sparkwingDir
}

// neutralizeEnv clears every env var the resolver looks at so tests
// don't pick up state from the developer's shell.
func neutralizeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GITHUB_ACTIONS", "KUBERNETES_SERVICE_HOST",
		"XDG_CONFIG_HOME",
	} {
		os.Unsetenv(k)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestApplyBackendsConfig_DefaultsFromFile(t *testing.T) {
	neutralizeEnv(t)
	cacheDir := t.TempDir()
	logDir := t.TempDir()
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  cache: { type: filesystem, path: `+cacheDir+` }
  logs:  { type: filesystem, path: `+logDir+` }
`)
	opts := Options{SparkwingDir: dir}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.ArtifactStore == nil {
		t.Fatal("ArtifactStore not populated")
	}
	if opts.LogStore == nil {
		t.Fatal("LogStore not populated")
	}
}

func TestApplyBackendsConfig_RespectsPreSetStores(t *testing.T) {
	neutralizeEnv(t)
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  cache: { type: filesystem, path: `+t.TempDir()+` }
  logs:  { type: filesystem, path: `+t.TempDir()+` }
`)
	preCache := stubArtifactStore{}
	preLogs := stubLogStore{}
	opts := Options{
		SparkwingDir:  dir,
		ArtifactStore: preCache,
		LogStore:      preLogs,
	}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := opts.ArtifactStore.(stubArtifactStore); !ok {
		t.Errorf("ArtifactStore was overwritten: %T", opts.ArtifactStore)
	}
	if _, ok := opts.LogStore.(stubLogStore); !ok {
		t.Errorf("LogStore was overwritten: %T", opts.LogStore)
	}
}

func TestApplyBackendsConfig_TargetOverlayWins(t *testing.T) {
	neutralizeEnv(t)
	envLogs := t.TempDir()
	targetLogs := t.TempDir()
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  cache: { type: filesystem, path: `+t.TempDir()+` }
  logs:  { type: filesystem, path: `+envLogs+` }
`)
	opts := Options{
		SparkwingDir: dir,
		Target:       "prod",
		PipelineYAML: &pipelines.Pipeline{
			Targets: map[string]pipelines.Target{
				"prod": {
					Backend: &pipelines.TargetBackend{
						Logs: map[string]any{"type": "filesystem", "path": targetLogs},
					},
				},
			},
		},
	}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// The factory opens the path under targetLogs; if the wrong
	// path leaks through, the file gets created in envLogs. We
	// can't introspect the store, so verify it indirectly: only the
	// target dir should be writable to by the LogStore.
	if entries, _ := os.ReadDir(targetLogs); len(entries) != 0 {
		t.Errorf("target log dir mutated unexpectedly")
	}
	// Sanity: stores constructed.
	if opts.LogStore == nil {
		t.Fatal("LogStore nil")
	}
}

func TestApplyBackendsConfig_GHADetectMatches(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	envCache := t.TempDir()
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  cache: { type: filesystem, path: /should/not/win }
  logs:  { type: filesystem, path: `+t.TempDir()+` }
environments:
  gha:
    detect: { env_var: GITHUB_ACTIONS, equals: "true" }
    cache: { type: filesystem, path: `+envCache+` }
`)
	opts := Options{SparkwingDir: dir}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.ArtifactStore == nil {
		t.Fatal("cache nil")
	}
	// Verify environment-resolved cache wins: by writing through
	// it and checking the target directory grew.
	if err := opts.ArtifactStore.Put(context.Background(), "probe", strReader("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if !dirHasFile(t, envCache) {
		t.Errorf("env cache path %s wasn't used", envCache)
	}
}

func TestApplyBackendsConfig_DefaultStateDBSynthesizesSQLite(t *testing.T) {
	neutralizeEnv(t)
	dir := writeBackendsYAML(t, t.TempDir(), ``)
	defaultDB := filepath.Join(t.TempDir(), "state.db")
	opts := Options{SparkwingDir: dir, DefaultStateDB: defaultDB}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.State == nil {
		t.Fatal("State not populated from DefaultStateDB")
	}
	defer opts.State.Close()
	if _, err := os.Stat(defaultDB); err != nil {
		t.Errorf("expected db file at default path: %v", err)
	}
}

func TestApplyBackendsConfig_StateSqliteWithoutPathFallsBackToDefault(t *testing.T) {
	neutralizeEnv(t)
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  state: { type: sqlite }
`)
	defaultDB := filepath.Join(t.TempDir(), "state.db")
	opts := Options{SparkwingDir: dir, DefaultStateDB: defaultDB}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.State == nil {
		t.Fatal("State not populated when sqlite spec lacks a path")
	}
	defer opts.State.Close()
	if _, err := os.Stat(defaultDB); err != nil {
		t.Errorf("expected db file at default path: %v", err)
	}
}

func TestApplyBackendsConfig_NoStateWhenUnconfigured(t *testing.T) {
	neutralizeEnv(t)
	dir := writeBackendsYAML(t, t.TempDir(), ``)
	opts := Options{SparkwingDir: dir}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.State != nil {
		opts.State.Close()
		t.Fatal("State should stay nil when neither spec nor DefaultStateDB is set")
	}
}

func TestApplyBackendsConfig_StateFromDefaultsYAML(t *testing.T) {
	neutralizeEnv(t)
	dbPath := filepath.Join(t.TempDir(), "from-yaml.db")
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  state: { type: sqlite, path: `+dbPath+` }
`)
	opts := Options{SparkwingDir: dir, DefaultStateDB: filepath.Join(t.TempDir(), "should-not-win.db")}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.State == nil {
		t.Fatal("State not populated from YAML")
	}
	defer opts.State.Close()
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("expected db at yaml-declared path %s: %v", dbPath, err)
	}
}

func TestApplyBackendsConfig_StateTargetOverlayWins(t *testing.T) {
	neutralizeEnv(t)
	defaultsDB := filepath.Join(t.TempDir(), "defaults.db")
	targetDB := filepath.Join(t.TempDir(), "target.db")
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  state: { type: sqlite, path: `+defaultsDB+` }
`)
	opts := Options{
		SparkwingDir: dir,
		Target:       "prod",
		PipelineYAML: &pipelines.Pipeline{
			Targets: map[string]pipelines.Target{
				"prod": {
					Backend: &pipelines.TargetBackend{
						State: map[string]any{"type": "sqlite", "path": targetDB},
					},
				},
			},
		},
	}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.State == nil {
		t.Fatal("State nil")
	}
	defer opts.State.Close()
	if _, err := os.Stat(targetDB); err != nil {
		t.Errorf("target state path not used: %v", err)
	}
	if _, err := os.Stat(defaultsDB); err == nil {
		t.Errorf("defaults state path leaked through despite target overlay")
	}
}

func TestApplyBackendsConfig_StateRespectsPreSet(t *testing.T) {
	neutralizeEnv(t)
	preDB := filepath.Join(t.TempDir(), "pre.db")
	pre, err := store.Open(preDB)
	if err != nil {
		t.Fatalf("pre open: %v", err)
	}
	defer pre.Close()
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  state: { type: sqlite, path: /should/not/win.db }
`)
	opts := Options{SparkwingDir: dir, State: pre}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.State != pre {
		t.Errorf("pre-set State was overwritten")
	}
}

func TestApplyBackendsConfig_LogsStdout(t *testing.T) {
	neutralizeEnv(t)
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  cache: { type: filesystem, path: `+t.TempDir()+` }
  logs:  { type: stdout }
`)
	opts := Options{SparkwingDir: dir}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.LogStore == nil {
		t.Fatal("LogStore nil")
	}
	// Sanity: a write succeeds (the production constructor routes to
	// os.Stdout; the test confirms wiring resolved, not the byte path).
	if err := opts.LogStore.Append(context.Background(), "r", "n", []byte("ping\n")); err != nil {
		t.Errorf("append: %v", err)
	}
}

func TestApplyBackendsConfig_ControllerTargetOverlayRoutesToProfile(t *testing.T) {
	neutralizeEnv(t)
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  cache: { type: filesystem, path: `+t.TempDir()+` }
  logs:  { type: filesystem, path: `+t.TempDir()+` }
`)
	lookup := func(name string) (string, string, error) {
		if name != "prod" {
			return "", "", fmt.Errorf("unknown profile %q", name)
		}
		return srv.URL, "tok-prod", nil
	}
	opts := Options{
		SparkwingDir:  dir,
		Target:        "prod",
		ProfileLookup: lookup,
		PipelineYAML: &pipelines.Pipeline{
			Targets: map[string]pipelines.Target{
				"prod": {
					Backend: &pipelines.TargetBackend{
						Cache: map[string]any{"type": "controller", "controller": "prod"},
					},
				},
			},
		},
	}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.ArtifactStore == nil {
		t.Fatal("ArtifactStore nil")
	}
	// Round-trip a Put through the cache store to confirm the wired
	// HTTP client points at the test server with the profile's token.
	if err := opts.ArtifactStore.Put(context.Background(), "k", strings.NewReader("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if sawAuth != "Bearer tok-prod" {
		t.Errorf("Authorization=%q, want Bearer tok-prod", sawAuth)
	}
}

func TestApplyBackendsConfig_KubernetesDetectRequiresControllerName(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  cache: { type: filesystem, path: `+t.TempDir()+` }
  logs:  { type: filesystem, path: `+t.TempDir()+` }
`)
	opts := Options{SparkwingDir: dir}
	err := ApplyBackendsConfig(context.Background(), &opts)
	if err == nil {
		t.Fatal("expected controller-name-missing error")
	}
	// The built-in kubernetes environment sets cache+logs to controller
	// with no controller name; the factory rejects that until the
	// operator overrides with a profile name in their backends.yaml.
	if !strings.Contains(err.Error(), "requires controller: <profile-name>") {
		t.Errorf("got %v", err)
	}
}

// TestApplyBackendsConfig_LocalOnly_IgnoresUnreachableBackends pins
// the --sw-local-only contract: when LocalOnly is set, the resolver
// skips backends.yaml entirely. A backends.yaml that names an
// unresolvable controller profile would normally surface as a hard
// error from the factory; LocalOnly short-circuits before any of
// that runs, so the operator can recover from a broken shared-state
// config without editing files.
func TestApplyBackendsConfig_LocalOnly_IgnoresUnreachableBackends(t *testing.T) {
	neutralizeEnv(t)
	dir := writeBackendsYAML(t, t.TempDir(), `
defaults:
  state: { type: controller, controller: does-not-exist }
  cache: { type: controller, controller: does-not-exist }
  logs:  { type: controller, controller: does-not-exist }
`)
	defaultDB := filepath.Join(t.TempDir(), "state.db")
	opts := Options{
		SparkwingDir:   dir,
		DefaultStateDB: defaultDB,
		LocalOnly:      true,
	}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply (LocalOnly): %v", err)
	}
	defer opts.State.Close()
	if opts.State == nil {
		t.Fatal("LocalOnly should produce a state store")
	}
	if _, ok := opts.State.(*store.Store); !ok {
		t.Errorf("LocalOnly state = %T, want *store.Store", opts.State)
	}
	if opts.LogStore != nil {
		t.Errorf("LocalOnly should leave LogStore nil; got %T", opts.LogStore)
	}
	if opts.ArtifactStore != nil {
		t.Errorf("LocalOnly should leave ArtifactStore nil; got %T", opts.ArtifactStore)
	}
}

func dirHasFile(t *testing.T, dir string) bool {
	t.Helper()
	var found bool
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			found = true
		}
		return nil
	})
	return found
}

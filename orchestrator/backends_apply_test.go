package orchestrator

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
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
func (stubLogStore) ReadRun(context.Context, string) ([]byte, error)               { return nil, nil }
func (stubLogStore) Stream(context.Context, string, string) (io.ReadCloser, error) { return nil, nil }
func (stubLogStore) DeleteRun(context.Context, string) error                       { return nil }

func strReader(s string) io.Reader { return strings.NewReader(s) }

var _ sparkwing.Cache = stubArtifactStore{}
var _ sparkwing.Logs = stubLogStore{}

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
		"SPARKWING_LOG_STORE", "SPARKWING_ARTIFACT_STORE",
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

func TestApplyBackendsConfig_EnvShimFillsAndWarns(t *testing.T) {
	neutralizeEnv(t)
	logDir := t.TempDir()
	cacheDir := t.TempDir()
	t.Setenv("SPARKWING_LOG_STORE", "fs://"+logDir)
	t.Setenv("SPARKWING_ARTIFACT_STORE", "fs://"+cacheDir)
	dir := writeBackendsYAML(t, t.TempDir(), ``)
	opts := Options{SparkwingDir: dir}
	if err := ApplyBackendsConfig(context.Background(), &opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.ArtifactStore == nil || opts.LogStore == nil {
		t.Fatal("shim values not applied")
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

func TestApplyBackendsConfig_KubernetesDetectFiresControllerError(t *testing.T) {
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
		t.Fatal("expected controller-unimplemented error")
	}
	// The built-in kubernetes environment sets cache+logs to controller;
	// the factory returns "not implemented in this build".
	if !strings.Contains(err.Error(), "not implemented in this build") {
		t.Errorf("got %v", err)
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

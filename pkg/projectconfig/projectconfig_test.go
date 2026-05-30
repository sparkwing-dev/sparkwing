package projectconfig_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func writeYAML(t *testing.T, dir, name, contents string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// A merged file that exercises every section.
const mergedFixture = `
profile: prod
profiles:
  prod:
    secrets: { type: controller, url: https://controller.prod.example.com, token_env: PROD_TOKEN }
    state:   { type: sqlite, path: .sparkwing/state.db }
    cache:   { type: filesystem, path: .sparkwing/cache }
    logs:    { type: controller, url: https://controller.prod.example.com, token_env: PROD_TOKEN }

pipelines:
  - name: release
    entrypoint: Release
    on:
      push:
        branches: [main]
        paths: ["cmd/**"]
    secrets:
      - {name: DEPLOY_TOKEN, required: true}

sparks:
  - name: sparks-core
    source: github.com/sparkwing-dev/sparks-core
    version: ^v0.10.0
`

func TestLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, projectconfig.Filename, mergedFixture)

	cfg, err := projectconfig.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Re-marshal the parsed config and parse it again: a stable
	// round-trip means the loader and the section types agree on shape.
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	path2 := writeYAML(t, t.TempDir(), projectconfig.Filename, string(out))
	cfg2, err := projectconfig.Load(path2)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reflect.DeepEqual(cfg, cfg2) {
		t.Fatalf("round-trip mismatch:\n first: %#v\nsecond: %#v", cfg, cfg2)
	}

	if len(cfg.Pipelines) != 1 || cfg.Pipelines[0].Name != "release" {
		t.Errorf("pipelines = %#v", cfg.Pipelines)
	}
	if cfg.Profile != "prod" {
		t.Errorf("project default profile = %q, want prod", cfg.Profile)
	}
	prod, ok := cfg.Profiles["prod"]
	if !ok || prod == nil {
		t.Fatalf("prod profile missing: %#v", cfg.Profiles)
	}
	if prod.Secrets == nil || prod.Secrets.URL != "https://controller.prod.example.com" {
		t.Errorf("prod secrets backend not parsed: %#v", prod.Secrets)
	}
	if prod.Secrets.TokenEnv != "PROD_TOKEN" {
		t.Errorf("prod secrets token_env not parsed: %#v", prod.Secrets)
	}
	if len(cfg.Sparks) != 1 || cfg.Sparks[0].Source != "github.com/sparkwing-dev/sparks-core" {
		t.Errorf("sparks = %#v", cfg.Sparks)
	}
}

func TestLoad_MissingFileReturnsNil(t *testing.T) {
	cfg, err := projectconfig.Load(filepath.Join(t.TempDir(), "sparkwing.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("missing file should return nil cfg, got %#v", cfg)
	}
}

func TestLoad_UnknownTopLevelFieldFails(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, projectconfig.Filename, "pipelnes:\n  - name: a\n    entrypoint: A\n")
	_, err := projectconfig.Load(path)
	if err == nil {
		t.Fatal("expected error on unknown top-level field")
	}
	if !strings.Contains(err.Error(), "pipelnes") {
		t.Errorf("error should name the unknown field: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should include the file path: %v", err)
	}
}

func TestLoad_SecretsBareStringRejected(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, projectconfig.Filename, `
pipelines:
  - name: release
    entrypoint: Release
    secrets: [DEPLOY_TOKEN]
`)
	_, err := projectconfig.Load(path)
	if err == nil {
		t.Fatal("expected the SecretsField migration error from inside the merged file")
	}
	if !strings.Contains(err.Error(), "bare string") {
		t.Errorf("want bare-string migration error, got: %v", err)
	}
}

// The next four tests assert each section parses identically to its
// standalone per-file loader on the same content.

func TestLoad_PipelinesMatchesParse(t *testing.T) {
	const bare = "pipelines:\n  - name: a\n    entrypoint: A\n  - name: b\n    entrypoint: B\n"
	want, err := pipelines.Parse(strings.NewReader(bare))
	if err != nil {
		t.Fatalf("pipelines.Parse: %v", err)
	}
	// The bare pipelines.yaml already nests its list under pipelines:,
	// so it doubles as the merged-file pipelines section verbatim.
	path := writeYAML(t, t.TempDir(), projectconfig.Filename, bare)
	cfg, err := projectconfig.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Pipelines, want.Pipelines) {
		t.Fatalf("pipelines mismatch:\n got %#v\nwant %#v", cfg.Pipelines, want.Pipelines)
	}
}

func TestLoad_SparksSection(t *testing.T) {
	const bareList = `  - name: sparks-core
    source: github.com/sparkwing-dev/sparks-core
    version: ^v0.10.0
  - name: my-sparks
    source: github.com/example/my-sparks
    version: latest
`
	want := []sparks.Library{
		{Name: "sparks-core", Source: "github.com/sparkwing-dev/sparks-core", Version: "^v0.10.0"},
		{Name: "my-sparks", Source: "github.com/example/my-sparks", Version: "latest"},
	}
	path := writeYAML(t, t.TempDir(), projectconfig.Filename, "sparks:\n"+bareList)
	cfg, err := projectconfig.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Sparks, want) {
		t.Fatalf("sparks mismatch:\n got %#v\nwant %#v", cfg.Sparks, want)
	}
}

func TestCheckLegacy_ErrorsOnStandaloneFiles(t *testing.T) {
	root := t.TempDir()
	sw := filepath.Join(root, ".sparkwing")
	writeYAML(t, sw, "pipelines.yaml", "pipelines: []\n")
	writeYAML(t, sw, "backends.yaml", "defaults: {}\n")
	err := projectconfig.CheckLegacy(root)
	if err == nil {
		t.Fatal("expected an error when legacy files are present")
	}
	for _, want := range []string{".sparkwing/pipelines.yaml", ".sparkwing/backends.yaml", projectconfig.Filename} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestCheckLegacy_SilentWhenOnlySparkwingYAML(t *testing.T) {
	root := t.TempDir()
	sw := filepath.Join(root, ".sparkwing")
	writeYAML(t, sw, projectconfig.Filename, "pipelines: []\n")
	if err := projectconfig.CheckLegacy(root); err != nil {
		t.Fatalf("migrated repo should be silent, got %v", err)
	}
}

func TestDiscover_WalksUp(t *testing.T) {
	root := t.TempDir()
	sw := filepath.Join(root, ".sparkwing")
	yamlPath := writeYAML(t, sw, projectconfig.Filename, "pipelines:\n  - name: a\n    entrypoint: A\n")
	deep := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	path, cfg, err := projectconfig.Discover(deep)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if path != yamlPath {
		t.Fatalf("path = %q, want %q", path, yamlPath)
	}
	if cfg == nil || len(cfg.Pipelines) != 1 || cfg.Pipelines[0].Name != "a" {
		t.Fatalf("cfg = %#v", cfg)
	}
}

func TestDiscover_NotFoundReturnsNilNilNil(t *testing.T) {
	path, cfg, err := projectconfig.Discover(t.TempDir())
	if err != nil {
		t.Fatalf("not-found should not error: %v", err)
	}
	if path != "" || cfg != nil {
		t.Fatalf("want empty path + nil cfg, got path=%q cfg=%#v", path, cfg)
	}
}

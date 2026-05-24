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
	"github.com/sparkwing-dev/sparkwing/pkg/runners"
	"github.com/sparkwing-dev/sparkwing/pkg/sources"
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

// A merged file that exercises every section plus the optional profile
// hint and a typed secrets block.
const mergedFixture = `
profile: shared-team

pipelines:
  - name: release
    entrypoint: Release
    on:
      push:
        branches: [main]
        paths: ["cmd/**"]
    secrets:
      - {name: DEPLOY_TOKEN, required: true}
    targets:
      prod:
        source: prod-secrets

runners:
  local:
    type: local
    labels: [local, "os=darwin"]
  cloud-linux:
    type: kubernetes
    controller: shared
    labels: [cloud-linux, "os=linux"]

sources:
  default: prod-secrets
  sources:
    prod-secrets:
      type: remote-controller
      controller: prod
    laptop-dotenv:
      type: file
      path: .env

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

	if cfg.Profile != "shared-team" {
		t.Errorf("profile = %q, want shared-team", cfg.Profile)
	}
	if len(cfg.Pipelines) != 1 || cfg.Pipelines[0].Name != "release" {
		t.Errorf("pipelines = %#v", cfg.Pipelines)
	}
	if cfg.Runners["local"].Name != "local" {
		t.Errorf("runner Name not stamped: %#v", cfg.Runners["local"])
	}
	if cfg.Sources == nil || cfg.Sources.Sources["prod-secrets"].Name != "prod-secrets" {
		t.Errorf("source Name not stamped: %#v", cfg.Sources)
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

func TestLoad_UnknownNestedFieldFails(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, projectconfig.Filename, "runners:\n  local:\n    type: local\n    bogus: true\n")
	_, err := projectconfig.Load(path)
	if err == nil {
		t.Fatal("expected error on unknown nested field")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("KnownFields should recurse into nested structs: %v", err)
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

func TestLoad_RunnersMatchesLoad(t *testing.T) {
	const bare = `runners:
  local:
    type: local
    labels: [local, "os=darwin"]
  cloud-linux:
    type: kubernetes
    controller: shared
    labels: [cloud-linux, "os=linux"]
    spec:
      resources:
        requests:
          cpu: "2"
`
	wantPath := writeYAML(t, t.TempDir(), "runners.yaml", bare)
	want, err := runners.Load(wantPath)
	if err != nil {
		t.Fatalf("runners.Load: %v", err)
	}
	path := writeYAML(t, t.TempDir(), projectconfig.Filename, bare)
	cfg, err := projectconfig.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Runners, want.Runners) {
		t.Fatalf("runners mismatch:\n got %#v\nwant %#v", cfg.Runners, want.Runners)
	}
}

func TestLoad_SourcesMatchesLoad(t *testing.T) {
	const bare = `default: team-vault
sources:
  team-vault:
    type: remote-controller
    controller: prod
  keychain:
    type: macos-keychain
    service: sparkwing
  dotenv:
    type: file
    path: .env
  shell:
    type: env
    prefix: SW_
`
	wantPath := writeYAML(t, t.TempDir(), "sources.yaml", bare)
	want, err := sources.Load(wantPath)
	if err != nil {
		t.Fatalf("sources.Load: %v", err)
	}
	// The merged file nests the whole sources.yaml File under sources:.
	merged := "sources:\n" + indent(bare, "  ")
	path := writeYAML(t, t.TempDir(), projectconfig.Filename, merged)
	cfg, err := projectconfig.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sources == nil {
		t.Fatal("sources section missing")
	}
	if !reflect.DeepEqual(*cfg.Sources, want) {
		t.Fatalf("sources mismatch:\n got %#v\nwant %#v", *cfg.Sources, want)
	}
}

func TestLoad_SparksMatchesLoadManifest(t *testing.T) {
	const bareList = `  - name: sparks-core
    source: github.com/sparkwing-dev/sparks-core
    version: ^v0.10.0
  - name: my-sparks
    source: github.com/example/my-sparks
    version: latest
`
	// sparks.yaml wraps the list under libraries:.
	manifestDir := t.TempDir()
	writeYAML(t, manifestDir, sparks.ManifestFilename, "libraries:\n"+bareList)
	want, err := sparks.LoadManifest(manifestDir)
	if err != nil {
		t.Fatalf("sparks.LoadManifest: %v", err)
	}
	// The merged file carries the same list directly under sparks:.
	path := writeYAML(t, t.TempDir(), projectconfig.Filename, "sparks:\n"+bareList)
	cfg, err := projectconfig.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Sparks, want.Libraries) {
		t.Fatalf("sparks mismatch:\n got %#v\nwant %#v", cfg.Sparks, want.Libraries)
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

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

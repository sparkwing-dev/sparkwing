package pipelines_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func TestParse_Minimal(t *testing.T) {
	yaml := `
pipelines:
  - name: lint
    entrypoint: Lint
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Pipelines) != 1 {
		t.Fatalf("expected 1 pipeline, got %d", len(cfg.Pipelines))
	}
	if cfg.Pipelines[0].Entrypoint != "Lint" {
		t.Fatalf("entrypoint = %q", cfg.Pipelines[0].Entrypoint)
	}
}

func TestParse_FullFeatures(t *testing.T) {
	yaml := `
pipelines:
  - name: build-test-deploy
    entrypoint: BuildTestDeploy
    on:
      push:
        branches: [main]
        env:
          TARGET: prod
      schedule: "0 */6 * * *"
      webhook:
        path: /hooks/btd
    secrets:
      - SPARKWING_ARGOCD_SERVER
      - SPARKWING_ARGOCD_TOKEN
    tags: [ci, deploy]
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Find("build-test-deploy")
	if p == nil {
		t.Fatal("pipeline not found")
	}
	if p.On.Push == nil || len(p.On.Push.Branches) != 1 || p.On.Push.Branches[0] != "main" {
		t.Fatalf("push branches mis-parsed: %+v", p.On.Push)
	}
	if p.On.Push.Env["TARGET"] != "prod" {
		t.Fatalf("push env mis-parsed")
	}
	if p.On.Schedule != "0 */6 * * *" {
		t.Fatalf("schedule mis-parsed: %q", p.On.Schedule)
	}
	if p.On.Webhook == nil || p.On.Webhook.Path != "/hooks/btd" {
		t.Fatalf("webhook mis-parsed: %+v", p.On.Webhook)
	}
	if len(p.Secrets) != 2 {
		t.Fatalf("secrets count = %d", len(p.Secrets))
	}
	// Legacy bare-string form maps to typed entries with Required=true.
	for _, e := range p.Secrets {
		if e.Name == "" {
			t.Fatalf("legacy bare-string secret produced empty Name: %+v", e)
		}
		if !e.Required {
			t.Fatalf("legacy bare-string secret should be Required, got %+v", e)
		}
	}
	if len(p.Tags) != 2 {
		t.Fatalf("tags count = %d", len(p.Tags))
	}
}

func TestParse_RejectsDuplicateName(t *testing.T) {
	yaml := `
pipelines:
  - name: lint
    entrypoint: A
  - name: lint
    entrypoint: B
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected duplicate-name error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParse_RejectsMissingEntrypoint(t *testing.T) {
	yaml := `
pipelines:
  - name: lint
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected missing-entrypoint error")
	}
}

func TestParse_RejectsMissingName(t *testing.T) {
	yaml := `
pipelines:
  - entrypoint: Lint
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected missing-name error")
	}
}

func TestParse_RejectsUnknownFields(t *testing.T) {
	yaml := `
pipelines:
  - name: lint
    entrypoint: Lint
    unknown_key: something
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func TestParse_Empty(t *testing.T) {
	cfg, err := pipelines.Parse(strings.NewReader(""))
	if err != nil {
		t.Fatalf("Parse empty: %v", err)
	}
	if len(cfg.Pipelines) != 0 {
		t.Fatalf("expected empty config, got %d", len(cfg.Pipelines))
	}
}

func TestDiscover_WalksUp(t *testing.T) {
	// Repo-like layout: /tmp/repo/.sparkwing/pipelines.yaml, start from /tmp/repo/sub/dir
	root := t.TempDir()
	sw := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(sw, 0o755); err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(sw, "pipelines.yaml")
	if err := os.WriteFile(yamlPath, []byte("pipelines:\n  - name: a\n    entrypoint: A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	path, cfg, err := pipelines.Discover(deep)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if path != yamlPath {
		t.Fatalf("path = %q, want %q", path, yamlPath)
	}
	if cfg.Find("a") == nil {
		t.Fatal("pipeline a missing")
	}
}

func TestDiscover_NotFound(t *testing.T) {
	_, _, err := pipelines.Discover(t.TempDir())
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestFindAndNames(t *testing.T) {
	yaml := `
pipelines:
  - name: lint
    entrypoint: Lint
  - name: release
    entrypoint: Release
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	names := cfg.Names()
	if len(names) != 2 || names[0] != "lint" || names[1] != "release" {
		t.Fatalf("names = %v", names)
	}
	if cfg.Find("missing") != nil {
		t.Fatal("unexpected match")
	}
}

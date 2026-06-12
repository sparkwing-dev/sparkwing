package pipelines_test

import (
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
      schedule: "0 */6 * * *"
      webhook:
        path: /hooks/btd
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
	if p.On.Schedule != "0 */6 * * *" {
		t.Fatalf("schedule mis-parsed: %q", p.On.Schedule)
	}
	if p.On.Webhook == nil || p.On.Webhook.Path != "/hooks/btd" {
		t.Fatalf("webhook mis-parsed: %+v", p.On.Webhook)
	}
}

func TestParse_GitHookTriggers(t *testing.T) {
	yaml := `
pipelines:
  - name: lint
    entrypoint: Lint
    on:
      pre_commit: {}
  - name: suite
    entrypoint: Suite
    on:
      pre_push: {}
  - name: self-install
    entrypoint: SelfInstall
    on:
      post_commit: {}
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p := cfg.Find("lint"); p == nil || p.On.PreHook == nil {
		t.Fatalf("pre_commit mis-parsed: %+v", p)
	}
	if p := cfg.Find("suite"); p == nil || p.On.PostHook == nil {
		t.Fatalf("pre_push mis-parsed: %+v", p)
	}
	if p := cfg.Find("self-install"); p == nil || p.On.PostCommitHook == nil {
		t.Fatalf("post_commit mis-parsed: %+v", p)
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

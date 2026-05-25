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
        values:
          target: prod
      schedule: "0 */6 * * *"
      webhook:
        path: /hooks/btd
    secrets:
      - {name: SPARKWING_ARGOCD_SERVER, required: true}
      - {name: SPARKWING_ARGOCD_TOKEN, required: true}
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
	if got := p.On.Push.Values["target"]; got != "prod" {
		t.Fatalf("push values[target] = %v, want prod", got)
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

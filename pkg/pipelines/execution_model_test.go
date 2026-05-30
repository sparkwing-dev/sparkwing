package pipelines_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func TestParse_PushTriggerBranchesAndPaths(t *testing.T) {
	yaml := `
pipelines:
  - name: release
    entrypoint: Release
    on:
      push:
        branches: [main]
        paths: ["cmd/**"]
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Find("release")
	if p == nil || p.On.Push == nil {
		t.Fatal("push trigger missing")
	}
	if len(p.On.Push.Branches) != 1 || p.On.Push.Branches[0] != "main" {
		t.Errorf("branches: %v", p.On.Push.Branches)
	}
	if len(p.On.Push.Paths) != 1 || p.On.Push.Paths[0] != "cmd/**" {
		t.Errorf("paths: %v", p.On.Push.Paths)
	}
}

func TestParse_BareStringSecrets_Rejected(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    secrets:
      - DEPLOY_TOKEN
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil || !strings.Contains(err.Error(), "bare string") {
		t.Fatalf("expected bare-string rejection; got %v", err)
	}
}

func TestParse_TypedSecretEntries(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    secrets:
      - {name: DEPLOY_TOKEN, required: true}
      - {name: SLACK_HOOK, optional: true}
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Find("deploy")
	if p == nil || len(p.Secrets) != 2 {
		t.Fatalf("secrets parse: %+v", p)
	}
	if !p.Secrets[0].Required || p.Secrets[1].Required {
		t.Errorf("required flags off: %+v", p.Secrets)
	}
}

func TestParse_SecretsRequiredAndOptionalRejected(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    secrets:
      - {name: X, required: true, optional: true}
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error; got %v", err)
	}
}

// --- v0.6 redesign: dispatch / guards / defaults / locked ----------

func TestParse_DispatchBlock(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy-prod
    entrypoint: Deploy
    dispatch:
      source:
        type: controller
        url: https://controller.prod.example.com
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Find("deploy-prod")
	if p == nil || p.Dispatch == nil {
		t.Fatalf("dispatch not parsed: %+v", p)
	}
	if p.Dispatch.Source == nil || p.Dispatch.Source.URL != "https://controller.prod.example.com" {
		t.Errorf("dispatch fields: %+v", p.Dispatch)
	}
}

func TestParse_GuardsAndDefaults(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy-prod
    entrypoint: Deploy
    guards:
      require: [profile-controller]
      reject:  [profile-local]
    defaults:
      image: "registry.prod.com/myapp:latest"
      replicas: "10"
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Find("deploy-prod")
	if p == nil {
		t.Fatal("pipeline missing")
	}
	if len(p.Guards.Require) != 1 || p.Guards.Require[0] != "profile-controller" {
		t.Errorf("guards.require: %+v", p.Guards.Require)
	}
	if p.Defaults["replicas"] != "10" {
		t.Errorf("defaults: %+v", p.Defaults)
	}
}

func TestParse_GuardsInvalidTokenRejected(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    guards:
      require: [something-bogus]
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil || !strings.Contains(err.Error(), "unknown token") {
		t.Fatalf("expected unknown-token rejection; got %v", err)
	}
}

// --- Unknown-field rejection (catch typos / removed-key holdovers) ---

func TestParse_UnknownPipelineFieldRejected(t *testing.T) {
	cases := []string{"targets", "args", "runners", "values", "locked", "completely_bogus"}
	for _, key := range cases {
		yaml := "pipelines:\n  - name: x\n    entrypoint: X\n    " + key + ": [a]\n"
		_, err := pipelines.Parse(strings.NewReader(yaml))
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("key %q: expected unknown-field error; got %v", key, err)
		}
	}
}

func TestPipelinesByEntrypoint_GroupsMultiple(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy-prod
    entrypoint: Deploy
  - name: deploy-dev
    entrypoint: Deploy
  - name: release
    entrypoint: Release
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	grouped := cfg.PipelinesByEntrypoint()
	if len(grouped["Deploy"]) != 2 {
		t.Errorf("Deploy entrypoint backed pipelines: %d, want 2", len(grouped["Deploy"]))
	}
	if len(grouped["Release"]) != 1 {
		t.Errorf("Release entrypoint backed pipelines: %d, want 1", len(grouped["Release"]))
	}
}

package pipelines_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func TestParse_PushTriggerValues_RoundTrip(t *testing.T) {
	yaml := `
pipelines:
  - name: release
    entrypoint: Release
    on:
      push:
        branches: [main]
        values:
          phase: prerelease
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Find("release")
	if p == nil {
		t.Fatal("pipeline not found")
	}
	if p.On.Push == nil {
		t.Fatal("push trigger missing")
	}
	if p.On.Push.Values["phase"] != "prerelease" {
		t.Errorf("push.values[phase] = %v, want prerelease", p.On.Push.Values["phase"])
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
      runners: [prod-pool]
      source: prod-secrets
      protected: true
      approvals: required
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Find("deploy-prod")
	if p == nil || p.Dispatch == nil {
		t.Fatalf("dispatch not parsed: %+v", p)
	}
	if p.Dispatch.Runners[0] != "prod-pool" {
		t.Errorf("runners: %+v", p.Dispatch.Runners)
	}
	if p.Dispatch.Source != "prod-secrets" || !p.Dispatch.Protected || p.Dispatch.Approvals != "required" {
		t.Errorf("dispatch fields: %+v", p.Dispatch)
	}
}

func TestParse_DispatchUnknownApprovalsRejected(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    dispatch:
      approvals: maybe
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil || !strings.Contains(err.Error(), "approvals") {
		t.Fatalf("expected approvals rejection; got %v", err)
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
    locked: [protected]
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
	if _, ok := p.LockedSet()["protected"]; !ok {
		t.Errorf("locked set: %+v", p.LockedSet())
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

func TestParse_LockedDuplicateRejected(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    locked: [protected, protected]
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected duplicate-lock rejection; got %v", err)
	}
}

// --- v0.6 migration errors -----------------------------------------

func TestParse_LegacyTargetsKeyRejectedWithMigrationMessage(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    targets:
      dev: {}
      prod: {runners: [prod-pool]}
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected `targets:` rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, "targets:") || !strings.Contains(msg, "v0.6") || !strings.Contains(msg, "Split into N pipelines") {
		t.Errorf("error message should name targets:, v0.6, and the split guidance; got %q", msg)
	}
}

func TestParse_V06ArgsBlockRejectedWithMigrationMessage(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    args:
      target:
        dev: {}
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil || !strings.Contains(err.Error(), "args:") || !strings.Contains(err.Error(), "v0.6") {
		t.Fatalf("expected args:-block rejection naming v0.6; got %v", err)
	}
}

func TestParse_TopLevelRunnersRejectedWithMigrationMessage(t *testing.T) {
	yaml := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    runners: [foo]
`
	_, err := pipelines.Parse(strings.NewReader(yaml))
	if err == nil || !strings.Contains(err.Error(), "dispatch:") {
		t.Fatalf("expected runners-under-dispatch guidance; got %v", err)
	}
}

// --- Values + round-trip ------------------------------------------

func TestParse_PipelineValuesLayered(t *testing.T) {
	yaml := `
pipelines:
  - name: app
    entrypoint: App
    values:
      base:   {region: us-west-2}
      runners:
        prod-pool: {region: eu-central-1}
`
	cfg, err := pipelines.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Find("app")
	if p == nil {
		t.Fatal("missing")
	}
	if p.Values.Base["region"] != "us-west-2" {
		t.Errorf("base: %v", p.Values.Base)
	}
	if p.Values.Runners["prod-pool"]["region"] != "eu-central-1" {
		t.Errorf("runners overlay: %v", p.Values.Runners)
	}
}

func TestPipeline_TriggerValues_NilPipelineSafe(t *testing.T) {
	var p *pipelines.Pipeline
	if got := p.TriggerValues("push"); got != nil {
		t.Errorf("nil-receiver should return nil; got %v", got)
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

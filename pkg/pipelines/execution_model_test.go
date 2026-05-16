package pipelines_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func TestParse_LegacyBareStringSecrets(t *testing.T) {
	src := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    secrets: [FOO, BAR]
`
	cfg, err := pipelines.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Pipelines[0]
	if len(p.Secrets) != 2 {
		t.Fatalf("count = %d", len(p.Secrets))
	}
	want := pipelines.SecretsField{
		{Name: "FOO", Required: true},
		{Name: "BAR", Required: true},
	}
	if !reflect.DeepEqual(p.Secrets, want) {
		t.Errorf("Secrets = %+v, want %+v", p.Secrets, want)
	}
}

func TestParse_TypedSecretEntries(t *testing.T) {
	src := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    secrets:
      - name: FOO
        required: true
      - name: BAZ
        optional: true
`
	cfg, err := pipelines.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := cfg.Pipelines[0].Secrets
	want := pipelines.SecretsField{
		{Name: "FOO", Required: true},
		{Name: "BAZ", Optional: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Secrets = %+v, want %+v", got, want)
	}
	if got[0].IsRequired() != true {
		t.Errorf("FOO.IsRequired() should be true")
	}
	if got[1].IsRequired() != false {
		t.Errorf("BAZ.IsRequired() should be false (Optional)")
	}
}

func TestParse_MixedSecretEntries(t *testing.T) {
	src := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    secrets:
      - BAR
      - name: FOO
        optional: true
`
	cfg, err := pipelines.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := cfg.Pipelines[0].Secrets
	want := pipelines.SecretsField{
		{Name: "BAR", Required: true},
		{Name: "FOO", Optional: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Secrets = %+v, want %+v", got, want)
	}
}

func TestParse_SecretsRequiredAndOptionalRejected(t *testing.T) {
	src := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    secrets:
      - name: FOO
        required: true
        optional: true
`
	_, err := pipelines.Parse(strings.NewReader(src))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

func TestParse_SecretsEmptyNameRejected(t *testing.T) {
	src := `
pipelines:
  - name: deploy
    entrypoint: Deploy
    secrets:
      - required: true
`
	_, err := pipelines.Parse(strings.NewReader(src))
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("expected empty-name error, got %v", err)
	}
}

func TestParse_TargetsAndRunners(t *testing.T) {
	src := `
pipelines:
  - name: release
    entrypoint: Release
    runners: [local, cloud-linux]
    targets:
      dev:
        values: { replicas: 1 }
      prod:
        runners: [prod-builders]
        approvals: required
        protected: true
        source: prod-vault
        values: { replicas: 5 }
      pi:
        runners: [local]
        source: local-keychain
        values: { device_serial: ABCD1234 }
`
	cfg, err := pipelines.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Pipelines[0]
	if !reflect.DeepEqual(p.Runners, []string{"local", "cloud-linux"}) {
		t.Errorf("Runners = %v", p.Runners)
	}
	if got := p.TargetNames(); !reflect.DeepEqual(got, []string{"dev", "pi", "prod"}) {
		t.Errorf("TargetNames = %v, want [dev pi prod]", got)
	}
	if !p.HasTarget("dev") || p.HasTarget("nope") {
		t.Errorf("HasTarget mismatch")
	}

	prod := p.Targets["prod"]
	if !reflect.DeepEqual(prod.Runners, []string{"prod-builders"}) {
		t.Errorf("prod.Runners = %v", prod.Runners)
	}
	if prod.Approvals != "required" {
		t.Errorf("prod.Approvals = %q", prod.Approvals)
	}
	if !prod.Protected {
		t.Errorf("prod.Protected should be true")
	}
	if prod.Source != "prod-vault" {
		t.Errorf("prod.Source = %q", prod.Source)
	}
	if v, _ := prod.Values["replicas"].(int); v != 5 {
		t.Errorf("prod.Values[replicas] = %v", prod.Values["replicas"])
	}
}

func TestParse_PipelineValuesLayered(t *testing.T) {
	src := `
pipelines:
  - name: release
    entrypoint: Release
    values:
      base:
        image_repo: example.dev/api
        replicas: 2
      runners:
        cloud-linux:
          replicas: 8
        local:
          replicas: 1
`
	cfg, err := pipelines.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	v := cfg.Pipelines[0].Values
	if v.Base["image_repo"] != "example.dev/api" {
		t.Errorf("Values.Base[image_repo] = %v", v.Base["image_repo"])
	}
	if v.Runners["cloud-linux"]["replicas"] != 8 {
		t.Errorf("Values.Runners[cloud-linux][replicas] = %v", v.Runners["cloud-linux"]["replicas"])
	}
	if v.Runners["local"]["replicas"] != 1 {
		t.Errorf("Values.Runners[local][replicas] = %v", v.Runners["local"]["replicas"])
	}
}

func TestParse_TargetBackend(t *testing.T) {
	src := `
pipelines:
  - name: release
    entrypoint: Release
    targets:
      prod:
        backend:
          cache:
            type: s3
            bucket: prod-cache
          logs:
            type: s3
            bucket: prod-logs
          state:
            type: postgres
            url: postgres://prod
`
	cfg, err := pipelines.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	be := cfg.Pipelines[0].Targets["prod"].Backend
	if be == nil {
		t.Fatalf("Backend nil")
	}
	if be.Cache["type"] != "s3" || be.Cache["bucket"] != "prod-cache" {
		t.Errorf("Cache = %v", be.Cache)
	}
	if be.Logs["bucket"] != "prod-logs" {
		t.Errorf("Logs = %v", be.Logs)
	}
	if be.State["type"] != "postgres" {
		t.Errorf("State = %v", be.State)
	}
}

func TestParse_TargetApprovalsUnknownRejected(t *testing.T) {
	src := `
pipelines:
  - name: release
    entrypoint: Release
    targets:
      prod:
        approvals: two-person
`
	_, err := pipelines.Parse(strings.NewReader(src))
	if err == nil || !strings.Contains(err.Error(), "approvals") {
		t.Fatalf("expected approvals error, got %v", err)
	}
}

func TestParse_TargetApprovalsRequiredAccepted(t *testing.T) {
	src := `
pipelines:
  - name: release
    entrypoint: Release
    targets:
      prod:
        approvals: required
`
	if _, err := pipelines.Parse(strings.NewReader(src)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

func TestParse_EmptyTargetBodyAccepted(t *testing.T) {
	src := `
pipelines:
  - name: release
    entrypoint: Release
    targets:
      dev: {}
`
	cfg, err := pipelines.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.Pipelines[0].HasTarget("dev") {
		t.Errorf("dev target missing")
	}
}

func TestParse_NoTargetsBlockAccepted(t *testing.T) {
	src := `
pipelines:
  - name: lint
    entrypoint: Lint
`
	cfg, err := pipelines.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Pipelines[0].Targets) != 0 {
		t.Errorf("expected zero targets, got %v", cfg.Pipelines[0].Targets)
	}
	if cfg.Pipelines[0].TargetNames() != nil {
		t.Errorf("TargetNames should be nil for no-targets pipeline")
	}
}

func TestParse_UnknownFieldAtPipelineRejected(t *testing.T) {
	src := `
pipelines:
  - name: lint
    entrypoint: Lint
    surprise: yes
`
	_, err := pipelines.Parse(strings.NewReader(src))
	if err == nil {
		t.Fatalf("expected KnownFields error")
	}
}

func TestParse_UnknownFieldAtTargetRejected(t *testing.T) {
	src := `
pipelines:
  - name: lint
    entrypoint: Lint
    targets:
      dev:
        oops: 1
`
	_, err := pipelines.Parse(strings.NewReader(src))
	if err == nil {
		t.Fatalf("expected KnownFields error at target level")
	}
}

func TestRoundTrip_FullSchema(t *testing.T) {
	src := `
pipelines:
  - name: release
    entrypoint: Release
    runners: [local, cloud-linux]
    secrets:
      - DEPLOY_TOKEN
      - name: SLACK_HOOK
        optional: true
    values:
      base:
        image_repo: example.dev/api
    targets:
      dev:
        values:
          replicas: 1
      prod:
        runners: [prod-builders]
        approvals: required
        protected: true
        source: prod-vault
        values:
          replicas: 5
`
	first, err := pipelines.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse first: %v", err)
	}
	out, err := yaml.Marshal(first)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	second, err := pipelines.Parse(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Parse second: %v\nyaml:\n%s", err, out)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("round-trip mismatch:\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

package orchestrator

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inspectCfg struct {
	ImageRepo string `sw:"image_repo" default:"default.dev/api"`
	Replicas  int    `sw:"replicas"`
	Region    string `sw:"region" default:"us-west-2"`
}
type inspectSec struct {
	DeployToken string `sw:"DEPLOY_TOKEN,required"`
	SlackHook   string `sw:"SLACK_HOOK"`
}
type inspectPipe struct{ sparkwing.Base }

func (inspectPipe) Config() any  { return &inspectCfg{} }
func (inspectPipe) Secrets() any { return &inspectSec{} }
func (inspectPipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}

func ensureInspectPipe(t *testing.T) *sparkwing.Registration {
	t.Helper()
	if reg, ok := sparkwing.Lookup("inspect-pipe"); ok {
		return reg
	}
	sparkwing.Register[sparkwing.NoInputs]("inspect-pipe",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return &inspectPipe{} })
	reg, _ := sparkwing.Lookup("inspect-pipe")
	return reg
}

func TestInspectPipelineConfig_Layering(t *testing.T) {
	reg := ensureInspectPipe(t)
	yamlEntry := &pipelines.Pipeline{
		Name: "inspect-pipe",
		Values: pipelines.PipelineValues{
			Base: map[string]any{"image_repo": "base.dev/api"},
		},
		Targets: map[string]pipelines.Target{
			"prod": {Values: map[string]any{"replicas": 5}},
		},
	}
	fields, err := sparkwing.InspectPipelineConfig(reg, yamlEntry, "prod", "")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	got := map[string]sparkwing.ConfigField{}
	for _, f := range fields {
		got[f.Name] = f
	}
	if got["image_repo"].Source != "pipelines.yaml values.base" {
		t.Errorf("image_repo source = %q", got["image_repo"].Source)
	}
	if got["image_repo"].Value != "base.dev/api" {
		t.Errorf("image_repo value = %v", got["image_repo"].Value)
	}
	if got["replicas"].Source != "pipelines.yaml targets.prod.values" {
		t.Errorf("replicas source = %q", got["replicas"].Source)
	}
	if got["region"].Source != "struct default" || got["region"].Value != "us-west-2" {
		t.Errorf("region = %+v", got["region"])
	}
}

func TestInspectPipelineConfig_UnsetField(t *testing.T) {
	reg := ensureInspectPipe(t)
	fields, err := sparkwing.InspectPipelineConfig(reg, nil, "", "")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, f := range fields {
		if f.Name == "replicas" && f.Source != "not set" {
			t.Errorf("replicas source = %q, want 'not set'", f.Source)
		}
	}
}

func TestInspectPipelineSecrets_UnionsYAMLAndStruct(t *testing.T) {
	reg := ensureInspectPipe(t)
	yamlEntry := &pipelines.Pipeline{
		Secrets: pipelines.SecretsField{
			{Name: "AUDIT_API_KEY", Required: true},
		},
	}
	fields, err := sparkwing.InspectPipelineSecrets(context.Background(), reg, yamlEntry, "team-vault")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	byName := map[string]sparkwing.SecretField{}
	for _, f := range fields {
		byName[f.Name] = f
	}
	if !byName["AUDIT_API_KEY"].Required || byName["AUDIT_API_KEY"].DeclaredIn != "pipelines.yaml secrets:" {
		t.Errorf("AUDIT_API_KEY = %+v", byName["AUDIT_API_KEY"])
	}
	if !byName["DEPLOY_TOKEN"].Required || byName["DEPLOY_TOKEN"].DeclaredIn != "Secrets() struct" {
		t.Errorf("DEPLOY_TOKEN = %+v", byName["DEPLOY_TOKEN"])
	}
	if byName["DEPLOY_TOKEN"].SourceName != "team-vault" {
		t.Errorf("SourceName not threaded: %+v", byName["DEPLOY_TOKEN"])
	}
	if byName["DEPLOY_TOKEN"].Note != "not resolved yet" {
		t.Errorf("DEPLOY_TOKEN.Note = %q (expected 'not resolved yet')", byName["DEPLOY_TOKEN"].Note)
	}
}

func TestInspectPipelineSecrets_ResolverHits(t *testing.T) {
	reg := ensureInspectPipe(t)
	resolver := sparkwing.SecretResolverFunc(func(_ context.Context, name string) (string, bool, error) {
		if name == "DEPLOY_TOKEN" {
			return "abc123", true, nil
		}
		return "", false, sparkwing.ErrSecretMissing
	})
	ctx := sparkwing.WithSecretResolver(context.Background(), resolver)
	fields, err := sparkwing.InspectPipelineSecrets(ctx, reg, nil, "team-vault")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, f := range fields {
		switch f.Name {
		case "DEPLOY_TOKEN":
			if !f.Resolved || f.Note != "" {
				t.Errorf("DEPLOY_TOKEN = %+v (want resolved)", f)
			}
		case "SLACK_HOOK":
			if f.Resolved || f.Note != "missing" {
				t.Errorf("SLACK_HOOK = %+v (want missing)", f)
			}
		}
	}
}

func TestPickSourceName(t *testing.T) {
	p := &pipelines.Pipeline{
		Targets: map[string]pipelines.Target{
			"prod": {Source: "prod-vault"},
			"dev":  {},
		},
	}
	if got := pickSourceName(p, "prod", ""); got != "prod-vault" {
		t.Errorf("prod target: got %q, want prod-vault", got)
	}
	if got := pickSourceName(p, "dev", ""); got != "" {
		t.Errorf("dev target (no binding, no defaults file): got %q, want empty", got)
	}
	if got := pickSourceName(nil, "", ""); got != "" {
		t.Errorf("nil pipeline: got %q, want empty", got)
	}
}

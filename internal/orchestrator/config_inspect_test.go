package orchestrator

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/sources"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inspectSec struct {
	DeployToken string `sw:"DEPLOY_TOKEN,required"`
	SlackHook   string `sw:"SLACK_HOOK"`
}
type inspectPipe struct{ sparkwing.Base }

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

func TestPipelineSourceLabel(t *testing.T) {
	withSource := &pipelines.Pipeline{
		Dispatch: &pipelines.Dispatch{Source: &sources.Source{Type: sources.TypeController, URL: "https://controller.prod.example.com"}},
	}
	withoutSource := &pipelines.Pipeline{}
	if got := pipelineSourceLabel(withSource); got != "controller:https://controller.prod.example.com" {
		t.Errorf("inline source: got %q, want controller:https://controller.prod.example.com", got)
	}
	if got := pipelineSourceLabel(withoutSource); got != "" {
		t.Errorf("no dispatch: got %q, want empty", got)
	}
	if got := pipelineSourceLabel(nil); got != "" {
		t.Errorf("nil pipeline: got %q, want empty", got)
	}
}

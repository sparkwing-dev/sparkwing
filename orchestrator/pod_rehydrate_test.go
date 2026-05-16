package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type podCfg struct {
	ImageRepo string `sw:"image_repo"`
	Replicas  int    `sw:"replicas"`
}
type podSec struct {
	Token string `sw:"DEPLOY_TOKEN,required"`
}
type podPipe struct{ sparkwing.Base }

func (podPipe) Config() any  { return &podCfg{} }
func (podPipe) Secrets() any { return &podSec{} }
func (podPipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}

var podRegistered bool

func ensurePodPipe(t *testing.T) *sparkwing.Registration {
	t.Helper()
	if reg, ok := sparkwing.Lookup("pod-rehydrate-pipe"); ok {
		return reg
	}
	sparkwing.Register[sparkwing.NoInputs]("pod-rehydrate-pipe",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return &podPipe{} })
	podRegistered = true
	reg, _ := sparkwing.Lookup("pod-rehydrate-pipe")
	return reg
}

func TestRehydratePipelineConfig_DecodesFromSnapshot(t *testing.T) {
	reg := ensurePodPipe(t)
	want := &podCfg{ImageRepo: "example.dev/api", Replicas: 5}
	cfgJSON, _ := json.Marshal(want)
	snap, _ := json.Marshal(planSnapshot{PipelineConfig: cfgJSON})
	got, err := rehydratePipelineConfig(snap, reg)
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	cfg := got.(*podCfg)
	if cfg.ImageRepo != "example.dev/api" || cfg.Replicas != 5 {
		t.Errorf("rehydrated cfg = %+v", cfg)
	}
}

func TestRehydratePipelineConfig_EmptySnapshotReturnsNil(t *testing.T) {
	reg := ensurePodPipe(t)
	got, err := rehydratePipelineConfig(nil, reg)
	if err != nil || got != nil {
		t.Fatalf("got %v / %v", got, err)
	}
}

func TestRehydratePipelineConfig_MalformedSnapshot(t *testing.T) {
	reg := ensurePodPipe(t)
	_, err := rehydratePipelineConfig([]byte("not json"), reg)
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestRehydratePipelineSecrets_ResolvesAgainstCtxResolver(t *testing.T) {
	reg := ensurePodPipe(t)
	resolver := sparkwing.SecretResolverFunc(func(_ context.Context, name string) (string, bool, error) {
		if name == "DEPLOY_TOKEN" {
			return "pod-resolved", true, nil
		}
		return "", false, sparkwing.ErrSecretMissing
	})
	ctx := sparkwing.WithSecretResolver(context.Background(), resolver)
	snap, _ := json.Marshal(planSnapshot{
		Secrets: pipelines.SecretsField{{Name: "DEPLOY_TOKEN", Required: true}},
	})
	got, err := rehydratePipelineSecrets(ctx, snap, reg)
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	sec := got.(*podSec)
	if sec.Token != "pod-resolved" {
		t.Errorf("Token = %q, want pod-resolved", sec.Token)
	}
}

func TestRehydratePipelineSecrets_MissingRequiredFails(t *testing.T) {
	reg := ensurePodPipe(t)
	resolver := sparkwing.SecretResolverFunc(func(context.Context, string) (string, bool, error) {
		return "", false, sparkwing.ErrSecretMissing
	})
	ctx := sparkwing.WithSecretResolver(context.Background(), resolver)
	snap, _ := json.Marshal(planSnapshot{
		Secrets: pipelines.SecretsField{{Name: "DEPLOY_TOKEN", Required: true}},
	})
	_, err := rehydratePipelineSecrets(ctx, snap, reg)
	if err == nil || !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Fatalf("expected ErrSecretMissing, got %v", err)
	}
}

func TestRehydratePipelineConfig_NilReg(t *testing.T) {
	got, err := rehydratePipelineConfig([]byte(`{"pipeline_config":{}}`), nil)
	if err != nil || got != nil {
		t.Fatalf("got %v / %v", got, err)
	}
}

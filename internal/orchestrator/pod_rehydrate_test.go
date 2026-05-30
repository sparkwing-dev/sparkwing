package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type podSec struct {
	Token string `sw:"DEPLOY_TOKEN,required"`
}
type podPipe struct{ sparkwing.Base }

func (podPipe) Secrets() any { return &podSec{} }
func (podPipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}

func ensurePodPipe(t *testing.T) *sparkwing.Registration {
	t.Helper()
	if reg, ok := sparkwing.Lookup("pod-rehydrate-pipe"); ok {
		return reg
	}
	sparkwing.Register[sparkwing.NoInputs]("pod-rehydrate-pipe",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return &podPipe{} })
	reg, _ := sparkwing.Lookup("pod-rehydrate-pipe")
	return reg
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
	got, err := rehydratePipelineSecrets(ctx, nil, reg)
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
	_, err := rehydratePipelineSecrets(ctx, nil, reg)
	if err == nil || !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Fatalf("expected ErrSecretMissing, got %v", err)
	}
}

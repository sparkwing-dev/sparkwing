package sparkwingruntime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// ensureRegistered registers `name` with `factory` if absent.
func ensureRegistered(t *testing.T, name string, factory func() sparkwing.Pipeline[sparkwing.NoInputs]) *sparkwing.Registration {
	t.Helper()
	if reg, ok := sparkwing.Lookup(name); ok {
		return reg
	}
	sparkwing.Register[sparkwing.NoInputs](name, factory)
	reg, ok := sparkwing.Lookup(name)
	if !ok {
		t.Fatalf("register/lookup race on %q", name)
	}
	return reg
}

type releaseSec struct {
	DeployToken string `sw:"DEPLOY_TOKEN,required"`
	SlackHook   string `sw:"SLACK_HOOK,optional"`
}

type secretsOnlyPipe struct{ sparkwing.Base }

func (secretsOnlyPipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}
func (secretsOnlyPipe) Secrets() any { return &releaseSec{} }

func registerSecretsOnlyPipe(t *testing.T) *sparkwing.Registration {
	return ensureRegistered(t, "secrets-only-pipe-rt", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &secretsOnlyPipe{}
	})
}

type releaseCfg struct {
	ImageRepo string `sw:"image_repo,required"`
	Replicas  int    `sw:"replicas" default:"2"`
}

type releasePipe struct{ sparkwing.Base }

func (releasePipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}
func (releasePipe) Config() any { return &releaseCfg{} }

func registerConfigOnlyPipe(t *testing.T) *sparkwing.Registration {
	return ensureRegistered(t, "config-only-pipe-rt", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &releasePipe{}
	})
}

type plainPipe struct{ sparkwing.Base }

func (plainPipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}

func registerPlainPipe(t *testing.T) *sparkwing.Registration {
	return ensureRegistered(t, "plain-pipe-rt", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &plainPipe{}
	})
}

// fakeResolver returns the values configured in its map; absent names
// resolve to sparkwing.ErrSecretMissing.
type fakeResolver struct {
	values map[string]string
	calls  []string
}

func (f *fakeResolver) Resolve(_ context.Context, name string) (string, bool, error) {
	f.calls = append(f.calls, name)
	v, ok := f.values[name]
	if !ok {
		return "", false, sparkwing.ErrSecretMissing
	}
	return v, true, nil
}

func TestResolvePipelineSecrets_RequiredResolved(t *testing.T) {
	reg := registerSecretsOnlyPipe(t)
	r := &fakeResolver{values: map[string]string{"DEPLOY_TOKEN": "swu_real"}}
	ctx := sparkwing.WithSecretResolver(context.Background(), r)

	out, err := sparkwingruntime.ResolvePipelineSecrets(ctx, reg, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sec := out.(*releaseSec)
	if sec.DeployToken != "swu_real" {
		t.Errorf("DeployToken = %q", sec.DeployToken)
	}
	if sec.SlackHook != "" {
		t.Errorf("SlackHook should stay empty (optional missing): %q", sec.SlackHook)
	}
}

func TestResolvePipelineSecrets_RequiredMissingFails(t *testing.T) {
	reg := registerSecretsOnlyPipe(t)
	r := &fakeResolver{values: map[string]string{}}
	ctx := sparkwing.WithSecretResolver(context.Background(), r)

	_, err := sparkwingruntime.ResolvePipelineSecrets(ctx, reg, nil)
	if err == nil || !strings.Contains(err.Error(), "DEPLOY_TOKEN") {
		t.Fatalf("expected DEPLOY_TOKEN error, got %v", err)
	}
}

func TestResolvePipelineSecrets_TransportErrorPropagates(t *testing.T) {
	bumpy := errors.New("vault unreachable")
	reg := registerSecretsOnlyPipe(t)
	r := sparkwing.SecretResolverFunc(func(_ context.Context, name string) (string, bool, error) {
		return "", false, bumpy
	})
	ctx := sparkwing.WithSecretResolver(context.Background(), r)

	_, err := sparkwingruntime.ResolvePipelineSecrets(ctx, reg, nil)
	if err == nil || !errors.Is(err, bumpy) {
		t.Fatalf("expected transport error to propagate, got %v", err)
	}
}

func TestResolvePipelineSecrets_NoProviderReturnsNil(t *testing.T) {
	reg := registerPlainPipe(t)
	out, err := sparkwingruntime.ResolvePipelineSecrets(context.Background(), reg, nil)
	if err != nil || out != nil {
		t.Fatalf("expected nil/nil, got %v/%v", out, err)
	}
}

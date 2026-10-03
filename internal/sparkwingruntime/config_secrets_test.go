package sparkwingruntime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

type plainPipe struct{ sparkwing.Base }

func (plainPipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}

func registerPlainPipe(t *testing.T) *sparkwing.Registration {
	return ensureRegistered(t, "plain-pipe-rt", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &plainPipe{}
	})
}

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

type coercionSecretPipe struct {
	plainPipe
	fields any
}

func (p coercionSecretPipe) Secrets() any { return p.fields }

func TestResolvePipelineSecrets_CoercionErrorsOmitValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields any
	}{
		{"required", &struct {
			Count int8 `sw:"COUNT,required"`
		}{}},
		{"optional", &struct {
			Count int8 `sw:"COUNT,optional"`
		}{}},
	} {
		for _, input := range []struct{ name, value string }{{"overflow", "12345"}, {"malformed", "private-invalid-number"}} {
			t.Run(tc.name+"/"+input.name, func(t *testing.T) {
				reg := ensureRegistered(t, "coercion-"+tc.name+"-"+input.name, func() sparkwing.Pipeline[sparkwing.NoInputs] {
					return coercionSecretPipe{fields: tc.fields}
				})
				ctx := sparkwing.WithSecretResolver(t.Context(), &fakeResolver{values: map[string]string{"COUNT": input.value}})
				out, err := sparkwingruntime.ResolvePipelineSecrets(ctx, reg, nil)
				if err == nil || out != nil {
					t.Fatalf("malformed secret resolved: %v, %v", out, err)
				}
				if strings.Contains(err.Error(), input.value) || !strings.Contains(err.Error(), "Count") || !strings.Contains(err.Error(), "int8") {
					t.Fatalf("unsafe or incomplete coercion error: %v", err)
				}
			})
		}
	}
}

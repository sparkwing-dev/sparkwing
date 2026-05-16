package sparkwing_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// ensureRegistered registers `name` with `factory` if absent. The
// sparkwing test suite includes TestPipelineVenue, which resets the
// global pipeline registry; init-time registrations don't survive
// that wipe. Calling this at the top of each test that needs a
// fixture restores the registration cleanly.
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

// --- Config-only pipeline -----------------------------------------------

type releaseCfg struct {
	ImageRepo string `sw:"image_repo,required"`
	Replicas  int    `sw:"replicas" default:"2"`
	Region    string `sw:"region" default:"us-west-2"`
}

type releasePipe struct{ sparkwing.Base }

func (releasePipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}
func (releasePipe) Config() any { return &releaseCfg{} }

func registerConfigOnlyPipe(t *testing.T) *sparkwing.Registration {
	return ensureRegistered(t, "config-only-pipe", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &releasePipe{}
	})
}

func TestResolvePipelineConfig_BaseAndTargetLayer(t *testing.T) {
	reg := registerConfigOnlyPipe(t)
	yamlEntry := &pipelines.Pipeline{
		Name:       "release",
		Entrypoint: "Release",
		Values: pipelines.PipelineValues{
			Base: map[string]any{"image_repo": "example.dev/api", "replicas": 1},
		},
		Targets: map[string]pipelines.Target{
			"prod": {Values: map[string]any{"replicas": 5}},
			"dev":  {Values: map[string]any{}},
		},
	}

	out, err := sparkwing.ResolvePipelineConfig(reg, yamlEntry, "prod")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := out.(*releaseCfg)
	if cfg.ImageRepo != "example.dev/api" {
		t.Errorf("ImageRepo = %q (base should fill)", cfg.ImageRepo)
	}
	if cfg.Replicas != 5 {
		t.Errorf("Replicas = %d (target should win over base)", cfg.Replicas)
	}
	if cfg.Region != "us-west-2" {
		t.Errorf("Region = %q (default should apply when neither yaml nor target sets)", cfg.Region)
	}
}

func TestResolvePipelineConfig_EmptyTargetUsesBaseOnly(t *testing.T) {
	reg := registerConfigOnlyPipe(t)
	yamlEntry := &pipelines.Pipeline{
		Values: pipelines.PipelineValues{
			Base: map[string]any{"image_repo": "foo/bar", "replicas": 3},
		},
	}
	out, err := sparkwing.ResolvePipelineConfig(reg, yamlEntry, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := out.(*releaseCfg)
	if cfg.Replicas != 3 {
		t.Errorf("Replicas = %d, want 3 (base)", cfg.Replicas)
	}
}

func TestResolvePipelineConfig_MissingRequiredFails(t *testing.T) {
	reg := registerConfigOnlyPipe(t)
	_, err := sparkwing.ResolvePipelineConfig(reg, &pipelines.Pipeline{}, "")
	if err == nil || !strings.Contains(err.Error(), "image_repo") {
		t.Fatalf("expected required-error for image_repo, got %v", err)
	}
}

func TestResolvePipelineConfig_TypeMismatchFails(t *testing.T) {
	reg := registerConfigOnlyPipe(t)
	yamlEntry := &pipelines.Pipeline{
		Values: pipelines.PipelineValues{
			Base: map[string]any{"image_repo": "x", "replicas": "two"},
		},
	}
	_, err := sparkwing.ResolvePipelineConfig(reg, yamlEntry, "")
	if err == nil || !strings.Contains(err.Error(), "Replicas") {
		t.Fatalf("expected coercion error on Replicas, got %v", err)
	}
}

// --- Tag-conflict registration error path --------------------------------

type badTagCfg struct {
	X string `sw:"x,required" default:"oops"`
}
type badTagPipe struct{ sparkwing.Base }

func (badTagPipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}
func (badTagPipe) Config() any { return &badTagCfg{} }

func registerBadTagPipe(t *testing.T) *sparkwing.Registration {
	return ensureRegistered(t, "bad-tag-pipe", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &badTagPipe{}
	})
}

func TestResolvePipelineConfig_RequiredAndDefaultRejected(t *testing.T) {
	reg := registerBadTagPipe(t)
	_, err := sparkwing.ResolvePipelineConfig(reg, &pipelines.Pipeline{}, "")
	if err == nil || !strings.Contains(err.Error(), "required and default") {
		t.Fatalf("expected required+default error, got %v", err)
	}
}

// --- Secrets-only pipeline -----------------------------------------------

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
	return ensureRegistered(t, "secrets-only-pipe", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &secretsOnlyPipe{}
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

	out, err := sparkwing.ResolvePipelineSecrets(ctx, reg, nil)
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

	_, err := sparkwing.ResolvePipelineSecrets(ctx, reg, nil)
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

	_, err := sparkwing.ResolvePipelineSecrets(ctx, reg, nil)
	if err == nil || !errors.Is(err, bumpy) {
		t.Fatalf("expected transport error to propagate, got %v", err)
	}
}

// --- Union: yaml SecretsField + struct fields ----------------------------

func TestResolvePipelineSecrets_UnionWithYAMLEntries(t *testing.T) {
	reg := registerSecretsOnlyPipe(t)
	r := &fakeResolver{values: map[string]string{
		"DEPLOY_TOKEN": "swu_x",
		"DATABASE_URL": "postgres://prod",
	}}
	ctx := sparkwing.WithSecretResolver(context.Background(), r)

	yamlEntry := &pipelines.Pipeline{
		Secrets: pipelines.SecretsField{
			{Name: "DATABASE_URL", Required: true}, // declared in yaml only
			{Name: "DEPLOY_TOKEN", Required: true}, // also declared in struct
		},
	}
	out, err := sparkwing.ResolvePipelineSecrets(ctx, reg, yamlEntry)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sec := out.(*releaseSec)
	if sec.DeployToken != "swu_x" {
		t.Errorf("DeployToken = %q", sec.DeployToken)
	}
	// DATABASE_URL is yaml-only -- not on the struct, but must still
	// resolve so a missing-required-secret fails the run.
	seen := map[string]bool{}
	for _, n := range r.calls {
		seen[n] = true
	}
	if !seen["DEPLOY_TOKEN"] || !seen["DATABASE_URL"] {
		t.Errorf("expected both names resolved, calls=%v", r.calls)
	}
}

func TestResolvePipelineSecrets_YamlOnlyRequiredMissingFails(t *testing.T) {
	reg := registerConfigOnlyPipe(t) // no SecretsProvider
	r := &fakeResolver{values: map[string]string{}}
	ctx := sparkwing.WithSecretResolver(context.Background(), r)

	yamlEntry := &pipelines.Pipeline{
		Secrets: pipelines.SecretsField{
			{Name: "DATABASE_URL", Required: true},
		},
	}
	_, err := sparkwing.ResolvePipelineSecrets(ctx, reg, yamlEntry)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("expected DATABASE_URL error, got %v", err)
	}
}

// --- Pipeline implementing neither ---------------------------------------

type plainPipe struct{ sparkwing.Base }

func (plainPipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}

func registerPlainPipe(t *testing.T) *sparkwing.Registration {
	return ensureRegistered(t, "plain-pipe", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &plainPipe{}
	})
}

func TestResolvePipelineConfig_NoProviderReturnsNil(t *testing.T) {
	reg := registerPlainPipe(t)
	out, err := sparkwing.ResolvePipelineConfig(reg, &pipelines.Pipeline{}, "")
	if err != nil || out != nil {
		t.Fatalf("expected nil/nil, got %v/%v", out, err)
	}
}

func TestResolvePipelineSecrets_NoProviderNoYamlReturnsNil(t *testing.T) {
	reg := registerPlainPipe(t)
	out, err := sparkwing.ResolvePipelineSecrets(context.Background(), reg, &pipelines.Pipeline{})
	if err != nil || out != nil {
		t.Fatalf("expected nil/nil, got %v/%v", out, err)
	}
}

// --- ctx accessors -------------------------------------------------------

func TestPipelineConfig_AccessorReturnsNilWhenAbsent(t *testing.T) {
	if got := sparkwing.PipelineConfig[releaseCfg](context.Background()); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestPipelineSecrets_AccessorReturnsNilWhenAbsent(t *testing.T) {
	if got := sparkwing.PipelineSecrets[releaseSec](context.Background()); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestPipelineConfig_RoundTrip(t *testing.T) {
	want := &releaseCfg{ImageRepo: "x", Replicas: 7}
	ctx := sparkwing.WithPipelineConfig(context.Background(), want)
	got := sparkwing.PipelineConfig[releaseCfg](ctx)
	if got == nil || got.ImageRepo != "x" || got.Replicas != 7 {
		t.Errorf("round-trip: %+v", got)
	}
}

func TestPipelineConfig_TypeMismatchPanics(t *testing.T) {
	ctx := sparkwing.WithPipelineConfig(context.Background(), &releaseCfg{})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on type mismatch")
		}
	}()
	_ = sparkwing.PipelineConfig[releaseSec](ctx)
}

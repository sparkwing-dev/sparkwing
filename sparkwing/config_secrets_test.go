package sparkwing_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// releaseSec is used by the PipelineSecrets[T] accessor tests below.
type releaseSec struct {
	DeployToken string `sw:"DEPLOY_TOKEN,required"`
	SlackHook   string `sw:"SLACK_HOOK,optional"`
}

// ensureRegistered registers `name` with `factory` if absent.
// Several SDK tests reset the global pipeline registry as part of
// their setup; init-time registrations don't survive that wipe.
// Calling this at the top of each test that needs a fixture
// restores the registration cleanly.
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

func TestResolvePipelineConfig_BaseLayer(t *testing.T) {
	reg := registerConfigOnlyPipe(t)
	yamlEntry := &pipelines.Pipeline{
		Name:       "release",
		Entrypoint: "Release",
		Values: pipelines.PipelineValues{
			Base: map[string]any{"image_repo": "example.dev/api", "replicas": 5},
		},
	}

	out, err := sparkwing.ResolvePipelineConfig(reg, yamlEntry, "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := out.(*releaseCfg)
	if cfg.ImageRepo != "example.dev/api" {
		t.Errorf("ImageRepo = %q (base should fill)", cfg.ImageRepo)
	}
	if cfg.Replicas != 5 {
		t.Errorf("Replicas = %d (base should fill)", cfg.Replicas)
	}
	if cfg.Region != "us-west-2" {
		t.Errorf("Region = %q (default should apply when yaml doesn't set)", cfg.Region)
	}
}

func TestResolvePipelineConfig_TriggerValuesWinOverBase(t *testing.T) {
	reg := registerConfigOnlyPipe(t)
	yamlEntry := &pipelines.Pipeline{
		Name:       "release",
		Entrypoint: "Release",
		Values: pipelines.PipelineValues{
			Base: map[string]any{"image_repo": "base.dev/api", "replicas": 1},
		},
		On: pipelines.Triggers{
			Push: &pipelines.PushTrigger{
				Values: map[string]any{
					"replicas":   9,
					"image_repo": "push.dev/api",
				},
			},
		},
	}
	out, err := sparkwing.ResolvePipelineConfig(reg, yamlEntry, "", "push")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := out.(*releaseCfg)
	if cfg.Replicas != 9 {
		t.Errorf("Replicas = %d, want 9 (trigger.values should win over base)", cfg.Replicas)
	}
	if cfg.ImageRepo != "push.dev/api" {
		t.Errorf("ImageRepo = %q, want push.dev/api (trigger.values fills base too)", cfg.ImageRepo)
	}
}

func TestResolvePipelineConfig_TriggerValuesNoopForUnmatchedSource(t *testing.T) {
	reg := registerConfigOnlyPipe(t)
	yamlEntry := &pipelines.Pipeline{
		Values: pipelines.PipelineValues{
			Base: map[string]any{"image_repo": "base.dev/api", "replicas": 3},
		},
		On: pipelines.Triggers{
			Push: &pipelines.PushTrigger{
				Values: map[string]any{"replicas": 99},
			},
		},
	}
	// triggerSource = "manual" means no spec matches → push.values is ignored.
	out, err := sparkwing.ResolvePipelineConfig(reg, yamlEntry, "", "manual")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := out.(*releaseCfg)
	if cfg.Replicas != 3 {
		t.Errorf("Replicas = %d, want 3 (manual trigger ignores push.values)", cfg.Replicas)
	}
}

func TestResolvePipelineConfig_EmptyTargetUsesBaseOnly(t *testing.T) {
	reg := registerConfigOnlyPipe(t)
	yamlEntry := &pipelines.Pipeline{
		Values: pipelines.PipelineValues{
			Base: map[string]any{"image_repo": "foo/bar", "replicas": 3},
		},
	}
	out, err := sparkwing.ResolvePipelineConfig(reg, yamlEntry, "", "")
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
	_, err := sparkwing.ResolvePipelineConfig(reg, &pipelines.Pipeline{}, "", "")
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
	_, err := sparkwing.ResolvePipelineConfig(reg, yamlEntry, "", "")
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
	_, err := sparkwing.ResolvePipelineConfig(reg, &pipelines.Pipeline{}, "", "")
	if err == nil || !strings.Contains(err.Error(), "required and default") {
		t.Fatalf("expected required+default error, got %v", err)
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
	out, err := sparkwing.ResolvePipelineConfig(reg, &pipelines.Pipeline{}, "", "")
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

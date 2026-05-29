package sparkwing

import (
	"context"
	"strings"
	"testing"
)

type resolveE2EArgs struct {
	Replicas int    `desc:"replicas"`
	Image    string `desc:"image"`
}

type resolveE2EJob struct {
	Base
	WithArgs[resolveE2EArgs]
}

func (j *resolveE2EJob) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

func (resolveE2EJob) Schema() (*Schema, error) {
	s := NewSchema[resolveE2EArgs]()
	s.Field("Replicas").Required().Range(1, 100)
	s.Field("Image").Default("nginx:latest")
	return s.Build()
}

func TestResolveAndBindJobArgs_BindsToWithArgsHolder(t *testing.T) {
	p := NewPlan()
	j := &resolveE2EJob{}
	Job(p, "deploy", j)

	in := ResolveInputs{
		FlagValues: map[string]string{"replicas": "5"},
	}
	merged, err := resolveAndBindJobArgs(p, in)
	if err != nil {
		t.Fatalf("resolveAndBindJobArgs: %v", err)
	}

	// The bound args should be readable via j.Args(ctx).
	a := j.Args(context.Background())
	if a.Replicas != 5 {
		t.Errorf("Replicas: got %d, want 5", a.Replicas)
	}
	if a.Image != "nginx:latest" {
		t.Errorf("Image (default): got %q, want nginx:latest", a.Image)
	}

	// Merged map should carry both fields keyed by flag name.
	if v, ok := merged["replicas"].(int); !ok || v != 5 {
		t.Errorf("merged[replicas]: got %v (ok=%v)", merged["replicas"], ok)
	}
	if v, ok := merged["image"].(string); !ok || v != "nginx:latest" {
		t.Errorf("merged[image]: got %v (ok=%v)", merged["image"], ok)
	}
}

func TestResolveAndBindJobArgs_RequiredErrorSurfacesWithJobID(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &resolveE2EJob{})

	// Missing required --replicas.
	_, err := resolveAndBindJobArgs(p, ResolveInputs{})
	if err == nil || !strings.Contains(err.Error(), "deploy") ||
		!strings.Contains(err.Error(), "replicas") {
		t.Fatalf("expected error naming the job and the arg; got %v", err)
	}
}

func TestResolveAndBindJobArgs_NilPlanIsNoop(t *testing.T) {
	merged, err := resolveAndBindJobArgs(nil, ResolveInputs{})
	if merged != nil || err != nil {
		t.Errorf("nil plan should be a no-op; got merged=%v err=%v", merged, err)
	}
}

func TestResolveAndBindJobArgs_PlanWithoutWithArgsJobsIsNoop(t *testing.T) {
	p := NewPlan()
	Job(p, "lint", func(_ context.Context) error { return nil })
	merged, err := resolveAndBindJobArgs(p, ResolveInputs{FlagValues: map[string]string{"x": "y"}})
	if merged != nil || err != nil {
		t.Errorf("plan with only func jobs should produce nil merged + nil err; got %v / %v", merged, err)
	}
}

func TestPlanResolvedArgs_StoresMergedAcrossJobs(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &resolveE2EJob{})

	if p.ResolvedArgs() != nil {
		t.Fatal("before resolveAndBindJobArgs, ResolvedArgs() should be nil")
	}

	merged, err := resolveAndBindJobArgs(p, ResolveInputs{FlagValues: map[string]string{"replicas": "3", "image": "redis:7"}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	p.setResolvedArgs(merged)

	got := p.ResolvedArgs()
	if got["replicas"].(int) != 3 || got["image"].(string) != "redis:7" {
		t.Errorf("ResolvedArgs round-trip mismatch: %+v", got)
	}
}

func TestWithResolvedArgs_RoundTripsThroughArgAccessor(t *testing.T) {
	merged := map[string]any{"target": "prod", "replicas": 7}
	ctx := WithResolvedArgs(context.Background(), merged)

	target, err := Arg[string](ctx, "target")
	if err != nil || target != "prod" {
		t.Errorf("Arg[string] via WithResolvedArgs: got (%q, %v)", target, err)
	}
	reps, err := Arg[int](ctx, "replicas")
	if err != nil || reps != 7 {
		t.Errorf("Arg[int] via WithResolvedArgs: got (%d, %v)", reps, err)
	}
}

func TestWithResolvedArgs_NilMapIsNoop(t *testing.T) {
	ctx := WithResolvedArgs(context.Background(), nil)
	if _, err := Arg[string](ctx, "anything"); err == nil {
		t.Error("nil map install should leave context without resolved args")
	}
}

func TestProfileResolutionFromContext_DefaultsFeedIntoResolver(t *testing.T) {
	p := NewPlan()
	j := &resolveE2EJob{}
	Job(p, "deploy", j)

	in := ResolveInputs{
		FlagValues:      map[string]string{},
		ProfileDefaults: map[string]string{"replicas": "9", "image": "redis:7"},
	}
	merged, err := resolveAndBindJobArgs(p, in)
	if err != nil {
		t.Fatalf("resolveAndBindJobArgs: %v", err)
	}
	a := j.Args(context.Background())
	if a.Replicas != 9 {
		t.Errorf("Replicas from profile defaults: got %d, want 9", a.Replicas)
	}
	if a.Image != "redis:7" {
		t.Errorf("Image from profile defaults: got %q, want redis:7", a.Image)
	}
	if v, _ := merged["replicas"].(int); v != 9 {
		t.Errorf("merged[replicas]: got %v, want 9", merged["replicas"])
	}
}

func TestProfileResolutionFromContext_FlagWinsOverProfileDefault(t *testing.T) {
	p := NewPlan()
	j := &resolveE2EJob{}
	Job(p, "deploy", j)

	in := ResolveInputs{
		FlagValues:      map[string]string{"replicas": "2"},
		ProfileDefaults: map[string]string{"replicas": "9", "image": "redis:7"},
	}
	if _, err := resolveAndBindJobArgs(p, in); err != nil {
		t.Fatalf("resolveAndBindJobArgs: %v", err)
	}
	a := j.Args(context.Background())
	if a.Replicas != 2 {
		t.Errorf("explicit flag should win over profile default; got %d, want 2", a.Replicas)
	}
	if a.Image != "redis:7" {
		t.Errorf("Image (profile default): got %q, want redis:7", a.Image)
	}
}

func TestProfileResolutionFromContext_ZeroValueIsNoop(t *testing.T) {
	pr := profileResolutionFromContext(context.Background())
	if pr.Defaults != nil || pr.Name != "" || pr.IsLocal {
		t.Errorf("zero-value context should produce zero ProfileResolutionContext; got %+v", pr)
	}
}

func TestProfileResolutionFromContext_RoundTrips(t *testing.T) {
	want := ProfileResolutionContext{
		Defaults: map[string]string{"target": "prod"},
		Name:     "prod",
		IsLocal:  false,
	}
	ctx := context.WithValue(context.Background(), keyProfileResolution, want)
	got := profileResolutionFromContext(ctx)
	if got.Name != want.Name || got.IsLocal != want.IsLocal || got.Defaults["target"] != "prod" {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

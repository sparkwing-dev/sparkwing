package sparkwing

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// jobargsArgs1 / jobargsArgs2 are deliberately disjoint so we can
// verify the union across two jobs in one plan.
type jobargsArgs1 struct {
	Replicas int    `desc:"replicas"`
	Image    string `desc:"image"`
}

type jobargsArgs2 struct {
	Webhook string `desc:"slack webhook"`
}

type jobargsJob1 struct {
	Base
	WithArgs[jobargsArgs1]
}

func (j *jobargsJob1) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

// jobargsJob1WithSchema declares constraints via SchemaProvider.
type jobargsJob1WithSchema struct {
	Base
	WithArgs[jobargsArgs1]
}

func (j *jobargsJob1WithSchema) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

func (jobargsJob1WithSchema) Schema() (*Schema, error) {
	s := NewSchema[jobargsArgs1]()
	s.Field("Replicas").Required()
	return s.Build()
}

// jobargsJob2 has a different args type; used to verify cross-job
// union without collisions.
type jobargsJob2 struct {
	Base
	WithArgs[jobargsArgs2]
}

func (j *jobargsJob2) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

// jobargsJobNoArgs has no WithArgs embedded.
type jobargsJobNoArgs struct {
	Base
}

func (j *jobargsJobNoArgs) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

// jobargsCollidingArgs declares a flag that collides with
// jobargsArgs1.Replicas (via the kebab-cased default).
type jobargsCollidingArgs struct {
	Replicas int `desc:"colliding name"`
}

type jobargsJobColliding struct {
	Base
	WithArgs[jobargsCollidingArgs]
}

func (j *jobargsJobColliding) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

// jobargsJobBadSchema returns an error from Schema().
type jobargsJobBadSchema struct {
	Base
	WithArgs[jobargsArgs1]
}

func (j *jobargsJobBadSchema) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

func (jobargsJobBadSchema) Schema() (*Schema, error) {
	return nil, errors.New("simulated build failure")
}

func TestJobArgs_RegistersSynthesizedSchema(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &jobargsJob1{})

	s := p.JobArgSchema("deploy")
	if s == nil {
		t.Fatal("expected synthesized schema for job embedding WithArgs[T]")
	}
	if s.GoType().Name() != "jobargsArgs1" {
		t.Errorf("schema GoType: got %s, want jobargsArgs1", s.GoType().Name())
	}
	if len(s.fields) != 2 {
		t.Errorf("expected 2 fields; got %d", len(s.fields))
	}
}

func TestJobArgs_RegistersAuthoredSchema(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &jobargsJob1WithSchema{})

	s := p.JobArgSchema("deploy")
	if s == nil {
		t.Fatal("expected schema for job that implements SchemaProvider")
	}
	if !s.field("Replicas").Required {
		t.Error("Required constraint from SchemaProvider should be in registered schema")
	}
}

func TestJobArgs_NoEntryForJobWithoutWithArgs(t *testing.T) {
	p := NewPlan()
	Job(p, "lint", &jobargsJobNoArgs{})
	if got := p.JobArgSchema("lint"); got != nil {
		t.Errorf("expected nil schema for job without WithArgs; got %v", got)
	}
}

func TestJobArgs_NoEntryForFunctionJob(t *testing.T) {
	p := NewPlan()
	Job(p, "fn", func(_ context.Context) error { return nil })
	if got := p.JobArgSchema("fn"); got != nil {
		t.Errorf("expected nil schema for function-style job; got %v", got)
	}
}

func TestJobArgs_TwoJobsDisjointArgsRegisterBoth(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &jobargsJob1{})
	Job(p, "notify", &jobargsJob2{})

	all := p.JobArgSchemas()
	if len(all) != 2 {
		t.Errorf("expected 2 registered schemas; got %d", len(all))
	}
	if _, ok := all["deploy"]; !ok {
		t.Error("deploy schema missing from JobArgSchemas")
	}
	if _, ok := all["notify"]; !ok {
		t.Error("notify schema missing from JobArgSchemas")
	}
}

func TestJobArgs_FlagCollisionPanics(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &jobargsJob1{})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on flag collision across two jobs")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "deploy") || !strings.Contains(msg, "colliding") || !strings.Contains(msg, "--replicas") {
			t.Errorf("collision panic should name both jobs and the flag; got %q", msg)
		}
	}()
	Job(p, "colliding", &jobargsJobColliding{})
}

func TestJobArgs_SchemaProviderErrorPanics(t *testing.T) {
	p := NewPlan()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when SchemaProvider returns error")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "simulated build failure") {
			t.Errorf("panic should include the underlying error; got %q", msg)
		}
	}()
	Job(p, "bad", &jobargsJobBadSchema{})
}

func TestJobArgs_TransitiveArgsSurface(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &jobargsJob1{})
	Job(p, "notify", &jobargsJob2{})

	surface := p.TransitiveArgsSurface()
	if len(surface) != 3 {
		t.Fatalf("expected 3 flags across both jobs; got %d (%v)", len(surface), keysOf(surface))
	}
	// Each flag should know which job owns it.
	if surface["replicas"].JobID != "deploy" {
		t.Errorf("--replicas should be owned by deploy; got %q", surface["replicas"].JobID)
	}
	if surface["webhook"].JobID != "notify" {
		t.Errorf("--webhook should be owned by notify; got %q", surface["webhook"].JobID)
	}
}

func TestJobArgs_EmptyPlanReturnsNilSurfaces(t *testing.T) {
	p := NewPlan()
	if got := p.JobArgSchemas(); got != nil {
		t.Errorf("empty plan should return nil schemas map; got %v", got)
	}
	if got := p.TransitiveArgsSurface(); got != nil {
		t.Errorf("empty plan should return nil transitive surface; got %v", got)
	}
}

func TestAssertJobArgsCoverage_AcceptsJobDeclaredFlags(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &jobargsJob1{})

	if err := assertJobArgsCoverage(p, map[string]string{"replicas": "3", "image": "nginx"}); err != nil {
		t.Errorf("flags declared by the job should pass; got %v", err)
	}
}

func TestAssertJobArgsCoverage_RejectsTypos(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &jobargsJob1{})

	err := assertJobArgsCoverage(p, map[string]string{"replicass": "3"})
	if err == nil || !strings.Contains(err.Error(), "replicass") {
		t.Errorf("typo should be flagged by name; got %v", err)
	}
}

func TestAssertJobArgsCoverage_NilPlanIsNoop(t *testing.T) {
	if err := assertJobArgsCoverage(nil, map[string]string{"anything": "x"}); err != nil {
		t.Errorf("nil plan should be a no-op; got %v", err)
	}
}

func keysOf(m map[string]TransitiveArg) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

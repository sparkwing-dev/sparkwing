package sparkwing

import (
	"context"
	"strings"
	"testing"
)

type jobargsArgs1 struct {
	Replicas int    `flag:"replicas" required:"true" desc:"replicas"`
	Image    string `flag:"image" default:"nginx:latest" desc:"image"`
	Mode     string `flag:"mode" short:"m" default:"rolling" enum:"rolling,recreate" desc:"rollout mode"`
	Scratch  string
}

type jobargsArgs2 struct {
	Webhook string `flag:"webhook" desc:"slack webhook"`
}

type jobargsJob1 struct {
	Base
	WithArgs[jobargsArgs1]
}

func (j *jobargsJob1) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

type jobargsJob2 struct {
	Base
	WithArgs[jobargsArgs2]
}

func (j *jobargsJob2) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

type jobargsJobNoArgs struct {
	Base
}

func (j *jobargsJobNoArgs) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

type jobargsCollidingArgs struct {
	Replicas int `flag:"replicas" desc:"colliding name"`
}

type jobargsJobColliding struct {
	Base
	WithArgs[jobargsCollidingArgs]
}

func (j *jobargsJobColliding) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

type jobargsSecretArgs struct {
	Token string `flag:"token" secret:"true"`
}

type jobargsJobSecret struct {
	Base
	WithArgs[jobargsSecretArgs]
}

func (j *jobargsJobSecret) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

type jobargsExtraArgs struct {
	Rest map[string]string `flag:",extra"`
}

type jobargsJobExtra struct {
	Base
	WithArgs[jobargsExtraArgs]
}

func (j *jobargsJobExtra) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", func(_ context.Context) error { return nil }), nil
}

func mustPanic(t *testing.T, want []string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		msg, _ := r.(string)
		for _, w := range want {
			if !strings.Contains(msg, w) {
				t.Errorf("panic %q should mention %q", msg, w)
			}
		}
	}()
	fn()
}

func TestJobArgs_DescribesTaggedFieldsOnly(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &jobargsJob1{})

	got := p.JobArgs()
	if len(got) != 3 {
		t.Fatalf("JobArgs = %+v, want the three flag-tagged fields", got)
	}
	byName := map[string]DescribeArg{}
	for _, a := range got {
		if a.JobID != "deploy" {
			t.Errorf("--%s JobID = %q, want deploy", a.Name, a.JobID)
		}
		byName[a.Name] = a
	}
	if !byName["replicas"].Required || byName["replicas"].Type != "int" {
		t.Errorf("--replicas = %+v, want a required int", byName["replicas"])
	}
	if byName["image"].Default != "nginx:latest" {
		t.Errorf("--image default = %q", byName["image"].Default)
	}
	mode := byName["mode"]
	if mode.Short != "m" || len(mode.Enum) != 2 {
		t.Errorf("--mode = %+v, want short m and a two-value enum", mode)
	}
}

func TestJobArgs_NoneForJobsWithoutWithArgs(t *testing.T) {
	p := NewPlan()
	Job(p, "lint", &jobargsJobNoArgs{})
	Job(p, "fn", func(_ context.Context) error { return nil })
	if got := p.JobArgs(); got != nil {
		t.Errorf("JobArgs = %+v, want nil", got)
	}
}

func TestJobArgs_FlagCollisionPanics(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &jobargsJob1{})
	mustPanic(t, []string{"deploy", "colliding", "--replicas"}, func() {
		Job(p, "colliding", &jobargsJobColliding{})
	})
}

func TestJobArgs_PipelineOnlyTagsPanic(t *testing.T) {
	mustPanic(t, []string{"secret", "pipeline Inputs"}, func() {
		Job(NewPlan(), "s", &jobargsJobSecret{})
	})
	mustPanic(t, []string{"extra", "pipeline Inputs"}, func() {
		Job(NewPlan(), "e", &jobargsJobExtra{})
	})
}

func TestResolveAndBindJobArgs_AppliesTagsAndBinds(t *testing.T) {
	p := NewPlan()
	deploy := &jobargsJob1{}
	notify := &jobargsJob2{}
	Job(p, "deploy", deploy)
	Job(p, "notify", notify)

	err := resolveAndBindJobArgs(p, map[string]string{"replicas": "5", "webhook": "https://hook", "pipeline-flag": "x"})
	if err != nil {
		t.Fatalf("resolveAndBindJobArgs: %v", err)
	}
	a := deploy.Args(context.Background())
	if a.Replicas != 5 || a.Image != "nginx:latest" || a.Mode != "rolling" {
		t.Errorf("deploy args = %+v", a)
	}
	if got := notify.Args(context.Background()).Webhook; got != "https://hook" {
		t.Errorf("notify webhook = %q", got)
	}
}

func TestResolveAndBindJobArgs_RequiredAndEnumFailNamingTheJob(t *testing.T) {
	cases := map[string]map[string]string{
		"missing required": {},
		"outside enum":     {"replicas": "1", "mode": "blue-green"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			p := NewPlan()
			Job(p, "deploy", &jobargsJob1{})
			err := resolveAndBindJobArgs(p, args)
			if err == nil || !strings.Contains(err.Error(), `job "deploy"`) {
				t.Fatalf("err = %v, want one naming job deploy", err)
			}
		})
	}
}

func TestAssertJobArgsCoverage(t *testing.T) {
	p := NewPlan()
	Job(p, "deploy", &jobargsJob1{})

	if err := assertJobArgsCoverage(p, map[string]string{"replicas": "3", "image": "nginx"}); err != nil {
		t.Errorf("flags declared by the job should pass; got %v", err)
	}
	err := assertJobArgsCoverage(p, map[string]string{"scratch": "3"})
	if err == nil || !strings.Contains(err.Error(), "--scratch") {
		t.Errorf("an untagged field is not a flag; got %v", err)
	}
	if err := assertJobArgsCoverage(nil, map[string]string{"anything": "x"}); err != nil {
		t.Errorf("nil plan should be a no-op; got %v", err)
	}
}

func TestWithArgs_ArgsPanicsBeforeBind(t *testing.T) {
	mustPanic(t, []string{"before"}, func() {
		_ = (&jobargsJob2{}).Args(context.Background())
	})
}

func TestEmbeddedArgs_NilAndNonStructInputs(t *testing.T) {
	for name, v := range map[string]any{
		"nil":            nil,
		"nil-ptr":        (*jobargsJob1)(nil),
		"non-ptr":        jobargsJob1{},
		"non-struct-ptr": new(int),
		"no-withargs":    &jobargsJobNoArgs{},
	} {
		if holder, argsType := embeddedArgs(v); holder != nil || argsType != nil {
			t.Errorf("%s: got holder=%v argsType=%v, want nil", name, holder, argsType)
		}
	}
}

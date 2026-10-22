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

	got := p.declaredArguments()
	if len(got) != 3 {
		t.Fatalf("JobArgs = %+v, want the three flag-tagged fields", got)
	}
	byName := map[string]describeArg{}
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
	if got := p.declaredArguments(); got != nil {
		t.Errorf("JobArgs = %+v, want nil", got)
	}
}

func TestJobArgs_SecretsRetainClassification(t *testing.T) {
	isolateProtocolDiscoveryRegistry(t)
	Register[NoInputs]("job-secrets", func() Pipeline[NoInputs] {
		return protocolDiscoveryPipeline[NoInputs]{declare: func(p *Plan, _ NoInputs) error {
			Job(p, "s", &jobargsJobSecret{})
			return nil
		}}
	})
	doc, err := (sdkOperations{}).Describe(t.Context())
	if err != nil || len(doc.Pipelines) != 1 {
		t.Fatalf("describe secret job arguments: %+v, %v", doc, err)
	}
	if args := doc.Pipelines[0].Args; len(args) != 1 || !args[0].Secret || args[0].JobID != "s" {
		t.Fatalf("secret job argument classification lost: %+v", args)
	}
}

func TestJobArgs_ExtraBagIsPipelineOnly(t *testing.T) {
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

	err := resolveAndBindJobArgs(p, map[string]string{"replicas": "5", "image": "nginx:latest", "mode": "rolling", "webhook": "https://hook", "pipeline-flag": "x"})
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

func TestWithArgs_BindFromAny(t *testing.T) {
	j := &jobargsJob2{}
	if err := j.bindFromAny(jobargsArgs2{Webhook: "https://hook"}); err != nil {
		t.Fatal(err)
	}
	if got := j.Args(context.Background()).Webhook; got != "https://hook" {
		t.Errorf("Webhook = %q", got)
	}
	if err := j.bindFromAny("wrong"); err == nil || !strings.Contains(err.Error(), "type mismatch") {
		t.Errorf("BindFromAny(wrong type) = %v, want a type mismatch", err)
	}
}

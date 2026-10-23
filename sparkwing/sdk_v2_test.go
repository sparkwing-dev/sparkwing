package sparkwing

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestV2Declarations(t *testing.T) {
	p := NewPlan().Priority(0).ClaimWait(0).Checkout(Checkout{Tags: new(bool)})
	n := Job(p, "build", func(context.Context) error { return nil }).
		Memo(func(context.Context) (MemoValue, error) { return MemoKey("build"), nil }, MemoTTL(0)).
		Cache(Npm()).Artifacts("dist/**").Env("MODE", "test").Env("CUSTOM_RUNTIME", "x", Startup()).Env("NODE_OPTIONS", "x").
		Retry(0, Backoff(time.Second)).Timeout(0)
	generated := n.Generate(func(_ context.Context, p *Plan) error {
		Job(p, "child", func(context.Context) error { return nil })
		return nil
	})
	p.Approval("release", ApprovalConfig{OnTimeout: ApprovalDeny}).Needs(n).NeedsGenerated(generated).SkipIf(func(context.Context) bool { return false })
	doc := planDocument(p)
	if len(doc.Jobs) != 2 || doc.Priority == nil || *doc.Priority != 0 || doc.ClaimWaitMS == nil {
		t.Fatalf("missing explicit settings: %+v", doc)
	}
	j := doc.Jobs[0]
	if j.Memo == nil || j.Memo.TTLMS == nil || *j.Memo.TTLMS != 0 || j.TimeoutMS == nil || j.Retry == nil {
		t.Fatalf("lost presence: %+v", j)
	}
	if len(j.Caches) != 1 || len(j.Artifacts) != 1 || j.Env["MODE"].Startup || !j.Env["CUSTOM_RUNTIME"].Startup || j.Env["NODE_OPTIONS"].Startup {
		t.Fatalf("declarations: %+v", j)
	}
	if doc.Jobs[1].Approval == nil || len(doc.Jobs[1].Steps) != 0 || len(doc.Jobs[1].Closures.SkipIf) != 1 {
		t.Fatalf("approval: %+v", doc.Jobs[1])
	}
}

func TestMemoKeyBoundaries(t *testing.T) {
	if reflect.DeepEqual(MemoKey("ab", "c"), MemoKey("a", "bc")) {
		t.Fatal("ambiguous key")
	}
	if !reflect.DeepEqual(MemoKey("value"), MemoKey("value")) {
		t.Fatal("unstable key")
	}
	if MemoKey("memo:none") == NoMemo {
		t.Fatal("literal part bypassed memoization")
	}
	if MemoKey(NoMemo) != NoMemo {
		t.Fatal("NoMemo did not propagate")
	}
}

func TestGeneratedCodeExecutesAndReplays(t *testing.T) {
	p := NewPlan()
	source := Job(p, "source", func(context.Context) error { return nil })
	calls := 0
	g := source.Generate(func(_ context.Context, expansion *Plan) error {
		calls++
		Job(expansion, "child", func(context.Context) error { return nil }).Generate(func(_ context.Context, nested *Plan) error {
			Job(nested, "grandchild", func(context.Context) error { return nil })
			return nil
		})
		return nil
	})
	ops := sdkOperations{}
	first, err := ops.Eval(context.Background(), p, nodeproto.Request{Job: "source", Closure: g.ID(), Input: json.RawMessage(`{}`)})
	if err != nil || first.Error != nil {
		t.Fatalf("generate: %v, %v", err, first.Error)
	}
	replay, err := ops.Eval(context.Background(), p, nodeproto.Request{Job: "source", Closure: g.ID(), Input: json.RawMessage(`{}`)})
	if err != nil || replay.Error != nil || calls != 1 || string(replay.Value) != string(first.Value) {
		t.Fatalf("replay: calls=%d %v %+v", calls, err, replay)
	}
	result, err := ops.RunStep(context.Background(), p, nodeproto.Request{Job: "child", Step: "run", Attempt: 1, Settled: []string{}})
	if err != nil || result.Outcome != "success" {
		t.Fatalf("generated body: %+v %v", result, err)
	}
	nested, err := ops.Eval(context.Background(), p, nodeproto.Request{Job: "child", Closure: "child/generate/0", Input: json.RawMessage(`{}`)})
	if err != nil || nested.Error != nil {
		t.Fatalf("nested: %+v %v", nested, err)
	}
	result, err = ops.RunStep(context.Background(), p, nodeproto.Request{Job: "grandchild", Step: "run", Attempt: 1, Settled: []string{}})
	if err != nil || result.Outcome != "success" {
		t.Fatalf("nested body: %+v %v", result, err)
	}
}

func TestGeneratedSettingsAreRefused(t *testing.T) {
	p := NewPlan()
	n := Job(p, "source", func(context.Context) error { return nil })
	g := n.Generate(func(_ context.Context, p *Plan) error { p.Priority(0); return nil })
	result, err := (sdkOperations{}).Eval(context.Background(), p, nodeproto.Request{Job: n.ID(), Closure: g.ID()})
	if err != nil || result.Error == nil {
		t.Fatalf("generation settings must fail: %+v %v", result, err)
	}
	if len(p.nodes) != 1 {
		t.Fatal("refused expansion changed executable topology")
	}
}

func TestNegativeDurationDoesNotBecomeZero(t *testing.T) {
	p := NewPlan()
	Job(p, "job", func(context.Context) error { return nil }).Timeout(-time.Nanosecond)
	raw, err := json.Marshal(planDocument(p))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker()}); err == nil {
		t.Fatal("negative duration became an allowed zero")
	}
}

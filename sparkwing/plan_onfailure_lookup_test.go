package sparkwing

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestOnFailureAppearsInPlanDocument(t *testing.T) {
	plan := NewPlan()
	Job(plan, "deploy", func(context.Context) error { return nil }).
		OnFailure("rollback", func(context.Context) error { return nil })
	jobs := planDocument(plan).Jobs
	if len(jobs) != 2 || jobs[0].ID != "deploy" || jobs[1].ID != "rollback" || jobs[1].OnFailureOf != "deploy" {
		t.Fatalf("recovery declarations = %+v", jobs)
	}
}

func TestRecoveryModifiersAndChainsTargetTheRecoveryJob(t *testing.T) {
	plan := NewPlan()
	parent := Job(plan, "deploy", func(context.Context) error { return nil })
	recovery := parent.OnFailure("rollback", func(context.Context) error { return nil })
	recovery.Memo(func(context.Context) (MemoValue, error) { return "rollback-v1", nil }).
		SkipIf(func(context.Context) bool { return true }).
		Concurrency(NewConcurrencyGroup("recover", ConcurrencyLimit{Capacity: 1, Scope: ScopeRun, OnLimit: Fail}))
	recovery.OnFailure("notify", func(context.Context) error { return nil })
	jobs := planDocument(plan).Jobs
	if len(jobs) != 3 || jobs[1].ID != "rollback" || jobs[1].OnFailureOf != "deploy" || jobs[2].ID != "notify" || jobs[2].OnFailureOf != "rollback" {
		t.Fatalf("recovery chain = %+v", jobs)
	}
	if jobs[0].Memo != nil || len(jobs[0].Closures.SkipIf) != 0 || len(jobs[0].Concurrency) != 0 {
		t.Fatalf("recovery modifiers changed the parent: %+v", jobs[0])
	}
	job := jobs[1]
	if job.Memo == nil || job.Closures.MemoKey == "" || len(job.Closures.SkipIf) != 1 || len(job.Concurrency) != 1 || job.Concurrency[0].Group != "recover" {
		t.Fatalf("recovery modifiers missing: %+v", job)
	}
	for closure, want := range map[string]string{job.Closures.MemoKey: `"rollback-v1"`, job.Closures.SkipIf[0]: `true`} {
		got, err := (sdkOperations{}).Eval(t.Context(), plan, nodeproto.Request{Closure: closure, Input: json.RawMessage(`null`)})
		if err != nil || got.Error != nil || string(got.Value) != want {
			t.Fatalf("%s = %+v, %v, want %s", closure, got, err, want)
		}
	}
}

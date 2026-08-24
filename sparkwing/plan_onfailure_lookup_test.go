package sparkwing

import (
	"context"
	"testing"
)

// A recovery node is part of the plan that declares it, so resolving
// it by id has to work. It never enters the plan's id index -- OnFailure
// builds it directly -- and while the only consumer was the dispatcher,
// which held the node pointer, nothing noticed. Anything that starts
// from an id alone (a node executing in its own process, replay) reads
// the miss as "not in this plan" and refuses to run the recovery.
func TestPlanJob_FindsOnFailureRecoveryNode(t *testing.T) {
	p := NewPlan()
	deploy := Job(p, "deploy", func(context.Context) error { return nil })
	deploy.OnFailure("rollback", func(context.Context) error { return nil })

	got := p.Job("rollback")
	if got == nil {
		t.Fatal("Job(\"rollback\") = nil; recovery node is unreachable by id")
	}
	if got.ID() != "rollback" {
		t.Fatalf("Job(\"rollback\").ID() = %q", got.ID())
	}
	if got != deploy.OnFailureNode() {
		t.Fatal("Job(\"rollback\") returned a different node than OnFailureNode()")
	}
}

func TestPlanJob_RegisteredNodesStillWinAndUnknownIsNil(t *testing.T) {
	p := NewPlan()
	deploy := Job(p, "deploy", func(context.Context) error { return nil })
	deploy.OnFailure("rollback", func(context.Context) error { return nil })

	if p.Job("deploy") != deploy {
		t.Error("Job(\"deploy\") did not return the registered node")
	}
	if p.Job("nope") != nil {
		t.Error("Job(\"nope\") should be nil")
	}
}

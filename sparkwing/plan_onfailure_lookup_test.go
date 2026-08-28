package sparkwing

import (
	"context"
	"testing"
)

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

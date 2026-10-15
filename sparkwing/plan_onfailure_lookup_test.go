package sparkwing

import (
	"context"
	"testing"
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

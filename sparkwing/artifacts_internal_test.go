package sparkwing

import (
	"context"
	"testing"
)

func TestArtifactDeclarationsReachPlanDocument(t *testing.T) {
	plan := NewPlan()
	producer := Job(plan, "build", func(context.Context) error { return nil }).Artifacts("dist/**")
	Job(plan, "deploy", func(context.Context) error { return nil }).Consumes(producer, Into("artifacts/build"))
	doc := planDocument(plan)
	if len(doc.Jobs) != 2 || len(doc.Jobs[0].Artifacts) != 1 || doc.Jobs[0].Artifacts[0] != "dist/**" {
		t.Fatalf("artifact outputs = %+v", doc.Jobs)
	}
	consumer := doc.Jobs[1]
	if len(consumer.Consumes) != 1 || consumer.Consumes[0].Producer != "build" || consumer.Consumes[0].Into != "artifacts/build" {
		t.Fatalf("artifact input = %+v", consumer.Consumes)
	}
	if len(consumer.Needs) != 1 || consumer.Needs[0] != "build" {
		t.Fatalf("artifact edge did not imply dependency: %+v", consumer.Needs)
	}
}

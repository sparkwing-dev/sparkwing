package sparkwingruntime_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestPreviewItem_Risks(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error { return nil })
	node := plan.Nodes()[0]
	step := node.Work().Steps()[0]
	step.Risk("destructive", "prod")

	preview, err := sparkwingruntime.PreviewPlan(plan, "deploy", nil, sparkwingruntime.PreviewOptions{})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Work == nil || len(preview.Nodes[0].Work.Steps) != 1 {
		t.Fatalf("unexpected preview shape: %+v", preview)
	}
	got := preview.Nodes[0].Work.Steps[0].Risks
	want := []string{"destructive", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PreviewItem.Risks = %v, want %v", got, want)
	}
}

func TestPreviewItem_RisksEmpty(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "plain", func(ctx context.Context) error { return nil })
	preview, err := sparkwingruntime.PreviewPlan(plan, "plain", nil, sparkwingruntime.PreviewOptions{})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	if len(preview.Nodes[0].Work.Steps[0].Risks) != 0 {
		t.Errorf("plain step should have empty Risks, got %v",
			preview.Nodes[0].Work.Steps[0].Risks)
	}
}

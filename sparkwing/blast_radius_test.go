package sparkwing

import (
	"context"
	"reflect"
	"testing"
)

// TestWorkStep_Destructive verifies the .Destructive() modifier
// records BlastRadiusDestructive on the step's marker set.
func TestWorkStep_Destructive(t *testing.T) {
	w := NewWork()
	s := Step(w, "apply", func(ctx context.Context) error { return nil }).Destructive()
	got := s.BlastRadius()
	want := []BlastRadius{BlastRadiusDestructive}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BlastRadius() = %v, want %v", got, want)
	}
}

// TestWorkStep_AffectsProduction verifies the AffectsProduction
// modifier records the matching marker.
func TestWorkStep_AffectsProduction(t *testing.T) {
	w := NewWork()
	s := Step(w, "touch-prod", func(ctx context.Context) error { return nil }).AffectsProduction()
	got := s.BlastRadius()
	want := []BlastRadius{BlastRadiusAffectsProduction}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BlastRadius() = %v, want %v", got, want)
	}
}

// TestWorkStep_CostsMoney verifies the CostsMoney modifier records
// the matching marker.
func TestWorkStep_CostsMoney(t *testing.T) {
	w := NewWork()
	s := Step(w, "spin-up-fleet", func(ctx context.Context) error { return nil }).CostsMoney()
	got := s.BlastRadius()
	want := []BlastRadius{BlastRadiusCostsMoney}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BlastRadius() = %v, want %v", got, want)
	}
}

// TestWorkStep_Combined verifies that chaining modifiers accumulates
// markers in declaration order.
func TestWorkStep_Combined(t *testing.T) {
	w := NewWork()
	s := Step(w, "destroy-prod-eks", func(ctx context.Context) error { return nil }).
		Destructive().
		AffectsProduction()
	got := s.BlastRadius()
	want := []BlastRadius{BlastRadiusDestructive, BlastRadiusAffectsProduction}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BlastRadius() = %v, want %v", got, want)
	}
}

// TestWorkStep_DuplicateDeclarationCollapses confirms calling the
// same modifier twice does not append a duplicate entry; the
// dispatcher cares about which markers fire, not the count.
func TestWorkStep_DuplicateDeclarationCollapses(t *testing.T) {
	w := NewWork()
	s := Step(w, "dup", func(ctx context.Context) error { return nil }).Destructive().Destructive()
	got := s.BlastRadius()
	want := []BlastRadius{BlastRadiusDestructive}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BlastRadius() = %v, want %v", got, want)
	}
}

// TestWorkStep_Default confirms an unmarked step returns an empty
// marker set, preserving zero-behavior-change for existing
// pipelines.
func TestWorkStep_Default(t *testing.T) {
	w := NewWork()
	s := Step(w, "plain", func(ctx context.Context) error { return nil })
	if got := s.BlastRadius(); len(got) != 0 {
		t.Errorf("BlastRadius() default = %v, want empty", got)
	}
}

// TestBlastRadius_String pins the canonical wire tokens so the
// describe cache and the dispatcher gate can never drift.
func TestBlastRadius_String(t *testing.T) {
	cases := []struct {
		in   BlastRadius
		want string
	}{
		{BlastRadiusDestructive, "destructive"},
		{BlastRadiusAffectsProduction, "production"},
		{BlastRadiusCostsMoney, "money"},
	}
	for _, tc := range cases {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("BlastRadius(%v).String() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBlastRadius_IsValid pins the canonical-marker membership test
// so the dispatcher's wire-decoder degrades gracefully on stale or
// future values.
func TestBlastRadius_IsValid(t *testing.T) {
	for _, m := range AllBlastRadii() {
		if !m.IsValid() {
			t.Errorf("AllBlastRadii includes invalid marker %v", m)
		}
	}
	if BlastRadius("garbage").IsValid() {
		t.Error("garbage marker should be invalid")
	}
	if BlastRadius("").IsValid() {
		t.Error("empty marker should be invalid")
	}
}

// TestBlastRadiusBlockedError_Message confirms the canonical error
// text matches the contract documented in the IMP-015 ticket so
// agents and humans see identical guidance.
func TestBlastRadiusBlockedError_Message(t *testing.T) {
	cases := []struct {
		marker BlastRadius
		want   string
	}{
		{
			BlastRadiusDestructive,
			`step "destroy-eks" in pipeline "cluster-down" is marked destructive; pass --allow-destructive to confirm or --dry-run to preview.`,
		},
		{
			BlastRadiusAffectsProduction,
			`step "touch-prod-db" in pipeline "migrate" is marked production; pass --allow-prod to confirm or --dry-run to preview.`,
		},
		{
			BlastRadiusCostsMoney,
			`step "spin-up-fleet" in pipeline "stress-test" is marked money; pass --allow-money to confirm or --dry-run to preview.`,
		},
	}
	for _, tc := range cases {
		var pipeline, step string
		switch tc.marker {
		case BlastRadiusDestructive:
			pipeline, step = "cluster-down", "destroy-eks"
		case BlastRadiusAffectsProduction:
			pipeline, step = "migrate", "touch-prod-db"
		case BlastRadiusCostsMoney:
			pipeline, step = "stress-test", "spin-up-fleet"
		}
		err := &BlastRadiusBlockedError{
			Pipeline: pipeline,
			StepID:   step,
			Marker:   tc.marker,
		}
		if got := err.Error(); got != tc.want {
			t.Errorf("Error() =\n  %q\nwant\n  %q", got, tc.want)
		}
	}
}

// TestPreviewItem_BlastRadius verifies PreviewPlan stringifies the
// per-step marker set onto PreviewItem.BlastRadius so JSON consumers
// (`pipeline plan --json`) see the contract alongside the runtime
// decision.
func TestPreviewItem_BlastRadius(t *testing.T) {
	plan := NewPlan()
	Job(plan, "deploy", JobFn(func(ctx context.Context) error { return nil }))
	// Grab the inner step and decorate it.
	node := plan.Nodes()[0]
	step := node.Work().Steps()[0]
	step.Destructive().AffectsProduction()

	preview, err := PreviewPlan(plan, "deploy", nil, PreviewOptions{})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Work == nil || len(preview.Nodes[0].Work.Steps) != 1 {
		t.Fatalf("unexpected preview shape: %+v", preview)
	}
	got := preview.Nodes[0].Work.Steps[0].BlastRadius
	want := []string{"destructive", "production"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PreviewItem.BlastRadius = %v, want %v", got, want)
	}
}

// TestPreviewItem_BlastRadiusEmpty confirms a plain step has no
// blast_radius field (omitempty wire shape).
func TestPreviewItem_BlastRadiusEmpty(t *testing.T) {
	plan := NewPlan()
	Job(plan, "plain", JobFn(func(ctx context.Context) error { return nil }))
	preview, err := PreviewPlan(plan, "plain", nil, PreviewOptions{})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	if len(preview.Nodes[0].Work.Steps[0].BlastRadius) != 0 {
		t.Errorf("plain step should have empty BlastRadius, got %v",
			preview.Nodes[0].Work.Steps[0].BlastRadius)
	}
}

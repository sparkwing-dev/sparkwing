package sparkwing

import (
	"context"
	"reflect"
	"testing"
)

func TestWorkStep_Risk_Single(t *testing.T) {
	w := NewWork()
	s := Step(w, "apply", func(ctx context.Context) error { return nil }).Risk("destructive")
	got := s.Risks()
	want := []string{"destructive"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Risks() = %v, want %v", got, want)
	}
}

func TestWorkStep_Risk_VariadicAccumulates(t *testing.T) {
	w := NewWork()
	s := Step(w, "destroy-prod-eks", func(ctx context.Context) error { return nil }).
		Risk("destructive", "prod")
	got := s.Risks()
	want := []string{"destructive", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Risks() = %v, want %v", got, want)
	}
}

func TestWorkStep_Risk_ChainedCallsAccumulate(t *testing.T) {
	w := NewWork()
	s := Step(w, "destroy", func(ctx context.Context) error { return nil }).
		Risk("destructive").
		Risk("prod")
	got := s.Risks()
	want := []string{"destructive", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Risks() = %v, want %v", got, want)
	}
}

func TestWorkStep_Risk_DuplicatesCollapse(t *testing.T) {
	w := NewWork()
	s := Step(w, "dup", func(ctx context.Context) error { return nil }).
		Risk("destructive").
		Risk("destructive", "destructive")
	got := s.Risks()
	want := []string{"destructive"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Risks() = %v, want %v", got, want)
	}
}

func TestWorkStep_Risk_EmptyLabelsIgnored(t *testing.T) {
	w := NewWork()
	s := Step(w, "ws", func(ctx context.Context) error { return nil }).
		Risk("", "  ", "destructive", "")
	got := s.Risks()
	want := []string{"destructive"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Risks() = %v, want %v", got, want)
	}
}

func TestWorkStep_Default(t *testing.T) {
	w := NewWork()
	s := Step(w, "plain", func(ctx context.Context) error { return nil })
	if got := s.Risks(); len(got) != 0 {
		t.Errorf("Risks() default = %v, want empty", got)
	}
}

func TestWorkStep_Risk_AuthorDefinedLabels(t *testing.T) {
	w := NewWork()
	s := Step(w, "rotate", func(ctx context.Context) error { return nil }).
		Risk("rotates-key", "kicks-everyone-off-vpn")
	got := s.Risks()
	want := []string{"rotates-key", "kicks-everyone-off-vpn"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Risks() = %v, want %v", got, want)
	}
}

func TestRiskBlockedError_Message(t *testing.T) {
	err := &RiskBlockedError{
		Pipeline:      "release-pi",
		StepID:        "destroy-eks",
		MissingLabels: []string{"destructive", "prod"},
	}
	want := `step "destroy-eks" in pipeline "release-pi" requires --sw-allow destructive,prod to confirm (or --sw-dry-run to preview).`
	if got := err.Error(); got != want {
		t.Errorf("Error() =\n  %q\nwant\n  %q", got, want)
	}
}

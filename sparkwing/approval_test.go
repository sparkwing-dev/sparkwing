package sparkwing

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestApproval_CreatesGateNode(t *testing.T) {
	plan := NewPlan()
	gate := JobApproval(plan, "approve-prod", ApprovalConfigOld{
		Message:  fmt.Sprintf("Promote %s to prod?", "abc123"),
		Timeout:  2 * time.Hour,
		OnExpiry: ApprovalDeny,
	})
	if !gate.Job().IsApproval() {
		t.Fatalf("IsApproval = false")
	}
	cfg := gate.Job().ApprovalConfigOld()
	if cfg == nil {
		t.Fatalf("ApprovalConfigOld is nil")
	}
	if cfg.Message != "Promote abc123 to prod?" {
		t.Errorf("Message = %q", cfg.Message)
	}
	if cfg.Timeout != 2*time.Hour {
		t.Errorf("Timeout = %v", cfg.Timeout)
	}
	if cfg.OnExpiry != ApprovalDeny {
		t.Errorf("OnExpiry = %q", cfg.OnExpiry)
	}
	if plan.Job("approve-prod") != gate.Job() {
		t.Errorf("plan.Job mismatch")
	}
}

func TestApproval_ZeroValueIsEmptyPolicy(t *testing.T) {
	plan := NewPlan()
	gate := JobApproval(plan, "g", ApprovalConfigOld{})
	if got := gate.Job().ApprovalConfigOld().OnExpiry; got != "" {
		t.Fatalf("default OnExpiry = %q, want zero value", got)
	}
}

func TestApproval_DuplicateIDPanics(t *testing.T) {
	plan := NewPlan()
	_ = JobApproval(plan, "g", ApprovalConfigOld{})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate id")
		}
	}()
	_ = JobApproval(plan, "g", ApprovalConfigOld{})
}

func TestApproval_EmptyIDPanics(t *testing.T) {
	plan := NewPlan()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on empty id")
		}
	}()
	_ = JobApproval(plan, "", ApprovalConfigOld{})
}

func TestApproval_InvalidOnExpiryPanics(t *testing.T) {
	plan := NewPlan()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on unknown policy")
		}
		s, _ := r.(string)
		if s == "" {
			if e, ok := r.(error); ok {
				s = e.Error()
			}
		}
		if s == "" {
			t.Fatalf("panic value not stringy: %T", r)
		}
	}()
	JobApproval(plan, "g", ApprovalConfigOld{OnExpiry: ApprovalTimeoutPolicy("not-a-real-policy")})
}

func TestApproval_RegularNodeIsNotApproval(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "x", &fakeJob{})
	if n.IsApproval() {
		t.Fatal("regular node reported as approval")
	}
	if n.ApprovalConfigOld() != nil {
		t.Fatal("regular node has non-nil ApprovalConfigOld")
	}
}

func TestApproval_GateNeedsAndChain(t *testing.T) {
	plan := NewPlan()
	upstream := Job(plan, "build", &fakeJob{})
	gate := JobApproval(plan, "approve", ApprovalConfigOld{Message: "?"}).
		Needs(upstream).
		SkipIf(func(context.Context) bool { return false })
	if got := gate.Job().DepIDs(); len(got) != 1 || got[0] != "build" {
		t.Fatalf("gate deps = %v, want [build]", got)
	}
	deploy := Job(plan, "deploy", &fakeJob{}).Needs(gate)
	if got := deploy.DepIDs(); len(got) != 1 || got[0] != "approve" {
		t.Fatalf("deploy deps = %v, want [approve]", got)
	}
}

type fakeJob struct{ Base }

func (fakeJob) Work(w *Work) (*WorkStep, error) {
	Step(w, "run", func(_ context.Context) error { return nil })
	return nil, nil
}

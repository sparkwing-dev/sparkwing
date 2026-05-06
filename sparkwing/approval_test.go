package sparkwing

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestApproval_CreatesGateNode(t *testing.T) {
	plan := NewPlan()
	gate := Job(plan, "approve-prod", &Approval{
		Message:  fmt.Sprintf("Promote %s to prod?", "abc123"),
		Timeout:  2 * time.Hour,
		OnExpiry: ApprovalDeny,
	})
	if !gate.IsApproval() {
		t.Fatalf("IsApproval = false")
	}
	cfg := gate.Approval()
	if cfg == nil {
		t.Fatalf("Approval is nil")
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
	if plan.Node("approve-prod") != gate {
		t.Errorf("plan.Node mismatch")
	}
}

func TestApproval_ZeroValueIsEmptyPolicy(t *testing.T) {
	plan := NewPlan()
	gate := Job(plan, "g", &Approval{})
	// The zero value of ApprovalTimeoutPolicy is "". The orchestrator
	// treats it as ApprovalFail at dispatch time -- authors who want
	// the default leave OnExpiry unset.
	if got := gate.Approval().OnExpiry; got != "" {
		t.Fatalf("default OnExpiry = %q, want zero value", got)
	}
}

func TestApproval_DuplicateIDPanics(t *testing.T) {
	plan := NewPlan()
	_ = Job(plan, "g", &Approval{})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate id")
		}
	}()
	_ = Job(plan, "g", &Approval{})
}

func TestApproval_EmptyIDPanics(t *testing.T) {
	plan := NewPlan()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on empty id")
		}
	}()
	_ = Job(plan, "", &Approval{})
}

// Approval.OnExpiry used to silently ignore policies it didn't
// recognize, leaving the gate on ApprovalFail without any signal to
// the caller. A typo or stale constant should panic at plan
// construction so the author sees it immediately.
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
	Job(plan, "g", &Approval{OnExpiry: ApprovalTimeoutPolicy("not-a-real-policy")})
}

func TestApproval_RegularNodeIsNotApproval(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "x", &fakeJob{})
	if n.IsApproval() {
		t.Fatal("regular node reported as approval")
	}
	if n.Approval() != nil {
		t.Fatal("regular node has non-nil Approval")
	}
}

type fakeJob struct{ Base }

func (fakeJob) Work() *Work {
	w := NewWork()
	w.Step("run", func(_ context.Context) error { return nil })
	return w
}

package sparkwing

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestApproval_CreatesGateNode(t *testing.T) {
	plan := NewPlan()
	gate := JobApproval(plan, "approve-prod", ApprovalConfig{
		Message:  fmt.Sprintf("Promote %s to prod?", "abc123"),
		Timeout:  2 * time.Hour,
		OnExpiry: ApprovalDeny,
	})
	if !gate.Node().IsApproval() {
		t.Fatalf("IsApproval = false")
	}
	cfg := gate.Node().ApprovalConfig()
	if cfg == nil {
		t.Fatalf("ApprovalConfig is nil")
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
	if plan.Node("approve-prod") != gate.Node() {
		t.Errorf("plan.Node mismatch")
	}
}

func TestApproval_ZeroValueIsEmptyPolicy(t *testing.T) {
	plan := NewPlan()
	gate := JobApproval(plan, "g", ApprovalConfig{})
	// The zero value of ApprovalTimeoutPolicy is "". The orchestrator
	// treats it as ApprovalFail at dispatch time -- authors who want
	// the default leave OnExpiry unset.
	if got := gate.Node().ApprovalConfig().OnExpiry; got != "" {
		t.Fatalf("default OnExpiry = %q, want zero value", got)
	}
}

func TestApproval_DuplicateIDPanics(t *testing.T) {
	plan := NewPlan()
	_ = JobApproval(plan, "g", ApprovalConfig{})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate id")
		}
	}()
	_ = JobApproval(plan, "g", ApprovalConfig{})
}

func TestApproval_EmptyIDPanics(t *testing.T) {
	plan := NewPlan()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on empty id")
		}
	}()
	_ = JobApproval(plan, "", ApprovalConfig{})
}

// ApprovalConfig.OnExpiry rejects unrecognized policies at plan
// construction so a typo or stale constant fails loud.
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
	JobApproval(plan, "g", ApprovalConfig{OnExpiry: ApprovalTimeoutPolicy("not-a-real-policy")})
}

func TestApproval_RegularNodeIsNotApproval(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "x", &fakeJob{})
	if n.IsApproval() {
		t.Fatal("regular node reported as approval")
	}
	if n.ApprovalConfig() != nil {
		t.Fatal("regular node has non-nil ApprovalConfig")
	}
}

// SDK-040: ApprovalGate exposes only the gate-appropriate modifiers.
// .Inline() / .Retry() / .Timeout() / .Cache() / .RunsOn() are not
// methods on *ApprovalGate -- the type system makes that class of
// mistake a compile error rather than a runtime panic / silent
// no-op. The negative cases would not compile, which is the point;
// this test exercises the positive shape.
func TestApproval_GateNeedsAndChain(t *testing.T) {
	plan := NewPlan()
	upstream := Job(plan, "build", &fakeJob{})
	gate := JobApproval(plan, "approve", ApprovalConfig{Message: "?"}).
		Needs(upstream).
		SkipIf(func(context.Context) bool { return false })
	if got := gate.Node().DepIDs(); len(got) != 1 || got[0] != "build" {
		t.Fatalf("gate deps = %v, want [build]", got)
	}
	// Downstream nodes can take *ApprovalGate as a Needs target too.
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

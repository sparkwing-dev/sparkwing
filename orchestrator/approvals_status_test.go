package orchestrator

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TestRenderApprovalsSection_PendingShowsPolicy verifies a pending
// gate row surfaces the on-timeout policy and a "running" wait age
// so an agent reading `runs status` knows what's holding the run.
func TestRenderApprovalsSection_PendingShowsPolicy(t *testing.T) {
	requested := time.Now().Add(-15 * time.Second)
	rows := []*store.Approval{{
		RunID:       "run-1",
		NodeID:      "gate",
		RequestedAt: requested,
		Message:     "deploy to prod?",
		OnTimeout:   store.ApprovalOnTimeoutFail,
	}}
	var buf bytes.Buffer
	renderApprovalsSection(&buf, rows)
	out := buf.String()
	for _, want := range []string{"approvals:", "gate", "pending", "fail", "running", "deploy to prod?"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendering missing %q in:\n%s", want, out)
		}
	}
}

// TestRenderApprovalsSection_ResolvedShowsApprover verifies a
// resolved gate surfaces approver, comment, and finite wait duration.
func TestRenderApprovalsSection_ResolvedShowsApprover(t *testing.T) {
	requested := time.Now().Add(-30 * time.Second)
	resolved := requested.Add(20 * time.Second)
	rows := []*store.Approval{{
		RunID:       "run-1",
		NodeID:      "gate",
		RequestedAt: requested,
		ResolvedAt:  &resolved,
		Resolution:  store.ApprovalResolutionApproved,
		Approver:    "alice",
		Comment:     "looked good",
		OnTimeout:   store.ApprovalOnTimeoutFail,
	}}
	var buf bytes.Buffer
	renderApprovalsSection(&buf, rows)
	out := buf.String()
	for _, want := range []string{"alice", "approved", "looked good", "20s"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendering missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "running") {
		t.Errorf("resolved gate should not show running wait: %s", out)
	}
}

// TestRenderApprovalsSection_EmptyIsNoop pins the no-approvals case
// to no output so it doesn't add a stray "approvals:" heading on
// every run.
func TestRenderApprovalsSection_EmptyIsNoop(t *testing.T) {
	var buf bytes.Buffer
	renderApprovalsSection(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("expected no output, got %q", buf.String())
	}
}

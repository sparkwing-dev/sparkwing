package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestReviewApprovalListsAndOutput(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		t.Run(format, func(t *testing.T) {
			f := reviewFixture()
			f.approvals = controllerwire.Approvals{Approvals: []controllerwire.Approval{{RunID: "run-stable", JobID: "ship", RequestedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), Message: "ship?", OnTimeout: "fail"}}}
			out, err := runReviewFake(t, f, []string{"runs", "approvals", "list", "--run", "412", "-o", format})
			if err != nil || len(f.selectors) != 1 || f.selectors[0] != "412" || !strings.Contains(out, "ship") || strings.Contains(out, "node_id") || strings.Contains(out, "NODE") {
				t.Fatalf("output=%s selectors=%v err=%v", out, f.selectors, err)
			}
			if format == "plain" && out != "run-stable/ship\n" {
				t.Fatalf("plain output %q", out)
			}
		})
	}
	f := reviewFixture()
	if _, err := runReviewFake(t, f, []string{"runs", "approvals", "list"}); err != nil || len(f.selectors) != 1 || f.selectors[0] != "" {
		t.Fatalf("pending selector: %v %v", f.selectors, err)
	}
}

func TestReviewApprovalDecisionUsesControllerActor(t *testing.T) {
	t.Setenv("USER", "forged-actor")
	for _, verb := range []string{"approve", "deny"} {
		f := reviewFixture()
		out, err := runReviewFake(t, f, []string{"runs", "approvals", verb, "--run", "412", "--job", "ship", "--comment", "reviewed", "-o", "json"})
		if err != nil || f.decision.IdempotencyKey == "" || f.decision.Comment != "reviewed" || f.selectors[0] != "412" || !strings.Contains(out, "server-actor") || !strings.Contains(out, `"replayed":true`) || strings.Contains(out, "forged-actor") {
			t.Fatalf("decision=%+v output=%s err=%v", f.decision, out, err)
		}
		want := "approved"
		if verb == "deny" {
			want = "denied"
		}
		if f.decision.Resolution != want {
			t.Fatalf("resolution %q", f.decision.Resolution)
		}
	}
}

package store

import (
	"fmt"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"math/rand"
	"testing"
	"testing/quick"
	"time"
)

func TestMeteredFailureReportSettlesAndReleasesClaimProperty(t *testing.T) {
	property := func(seed uint32, cloud, started bool) bool {
		s, demand, now := meteredUnitFixture(t, cloud)
		_, actor := meteredUnitClaim(t, s, demand)
		tenant, ref := &Tenant{s: s, team: "a"}, controllerwire.NodeRef{RunID: "run", NodeID: "job"}
		attempt := int64(0)
		if started {
			applied, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: ref, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)})
			if err != nil {
				t.Fatal(err)
			}
			attempt = applied.Attempt
		}
		*now = now.Add(time.Duration(seed%15+1) * time.Second)
		reason := []string{FailureCreditsExhausted, FailureComputeLimit}[seed%2]
		message := fmt.Sprintf("stopped-%d", seed)
		change := NodeTransition{Ref: ref, Actor: actor, To: NodeFailed, Cause: CauseReport, SourceAttempt: sourceAttempt(attempt), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeFailed, Reason: reason, MaskedError: message}}}
		for replay := range 2 {
			applied, err := tenant.TransitionNode(t.Context(), change)
			if err != nil || applied.Replayed != (replay == 1) {
				t.Fatalf("failure report: %+v: %v", applied, err)
			}
		}
		row, err := tenant.ReadNode(t.Context(), ref)
		if err != nil || row.Status != StatusDone || row.Outcome != NodeFailed || row.ClaimedBy != "" || row.CapabilityPrefix != "" || row.LeaseExpiresAt != nil || row.Attempt != attempt || row.FinishedAt == nil || !row.FinishedAt.Equal(*now) {
			t.Fatalf("failure retained claim: %+v: %v", row, err)
		}
		var savedReason, savedMessage string
		if err := s.db.QueryRow("SELECT failure_reason,error FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'").Scan(&savedReason, &savedMessage); err != nil || savedReason != reason || savedMessage != message {
			t.Fatalf("failure reason=%q message=%q: %v", savedReason, savedMessage, err)
		}
		want := int64(600)
		if cloud && !started {
			want = 0
		}
		unitBillingTotal(t, s, want, true)
		if started {
			var began, finished int64
			if err := s.db.QueryRow("SELECT started_at,finished_at FROM node_attempts WHERE team='a' AND run_id='run' AND node_id='job'").Scan(&began, &finished); err != nil || finished < began || finished != now.UnixNano() {
				t.Fatalf("attempt ended before execution: %d -> %d: %v", began, finished, err)
			}
		}
		var active int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM billable_intervals WHERE finished_at IS NULL").Scan(&active); err != nil || active != 0 {
			t.Fatalf("failure retained compute: %d: %v", active, err)
		}
		var events int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM events WHERE team='a' AND run_id='run' AND node_id='job'").Scan(&events); err != nil {
			t.Fatal(err)
		}
		change.Extra.Result.MaskedError += "-changed"
		if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
			t.Fatal("changed failure replay committed")
		}
		unitBillingTotal(t, s, want, true)
		var after int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM events WHERE team='a' AND run_id='run' AND node_id='job'").Scan(&after); err != nil || after != events {
			t.Fatalf("changed replay added events: %d -> %d: %v", events, after, err)
		}
		return true
	}
	for _, cloud := range []bool{false, true} {
		for _, started := range []bool{false, true} {
			for seed := range uint32(2) {
				property(seed, cloud, started)
			}
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24, Rand: rand.New(rand.NewSource(744))}); err != nil {
		t.Fatal(err)
	}
}

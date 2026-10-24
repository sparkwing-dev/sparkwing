package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestStoreBillingStartAwardVersusStart(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		t.Run(map[bool]string{false: "runner", true: "cloud"}[cloud], func(t *testing.T) {
			s, demand, now := meteredUnitFixture(t, cloud)
			path := blobAccountingDiskStore(t, s)
			awardedAt := *now
			claim, actor := meteredUnitClaim(t, s, demand)
			principal, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
			if err != nil {
				t.Fatal(err)
			}
			provenance := principal.Capability.Billing
			start, executor := capability.BillingAtAward, "runner"
			if cloud {
				start, executor = capability.BillingAtStart, "cloud"
			}
			if provenance == nil || provenance.PayerTeam != "a" || !provenance.Metered || provenance.Start != start || provenance.ExecutorClass != executor || provenance.Unit != capability.BillingJob {
				t.Fatalf("award provenance: %+v", provenance)
			}
			intervals := 1
			if cloud {
				intervals = 0
			}
			rewriteSchemaCount(t, s.db, "billable_intervals", intervals)
			table := CreditRateTable{{Cores: 1, MicroPerSecond: 200}, {Cores: 4, MicroPerSecond: 1000}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(5 * time.Second)
			startedAt := *now
			tenant := &Tenant{s: s, team: claim.Team}
			ref := runspec.NodeRef{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID)}
			admitExecution(t, tenant, actor)
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
			if err := s.db.Close(); err != nil {
				t.Fatal(err)
			}
			s.db = blobAccountingDB(t, path)
			row, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || row.Status != runstate.StatusRunning || row.Attempt != 1 || row.CapabilityPrefix != actor.CapabilityPrefix || row.StartedAt == nil || !row.StartedAt.Equal(startedAt) {
				t.Fatalf("reopened execution: %+v, %v", row, err)
			}
			var award, began, price, reserved int64
			err = s.db.QueryRowContext(t.Context(), `SELECT a.awarded_at,i.started_at,c.rate_micro_per_second,c.amount_micro FROM billing_awards a JOIN billable_intervals i ON i.team=a.team AND i.reservation_id=a.reservation_id JOIN credit_charges c ON c.team=a.team AND c.capability_prefix=a.capability_prefix WHERE a.team=? AND a.reservation_id=? AND c.kind='reservation'`, claim.Team, provenance.ReservationID).Scan(&award, &began, &price, &reserved)
			wantStart := awardedAt
			wantPrice := int64(2)
			if cloud {
				wantStart = startedAt
				wantPrice = 10
			}
			if err != nil || award != awardedAt.UnixNano() || began != wantStart.UnixNano() || price != wantPrice || reserved != 60*wantPrice {
				t.Fatalf("reopened billing: award=%d start=%d price=%d reserved=%d: %v", award, began, price, reserved, err)
			}
			*now = now.Add(5 * time.Second)
			change := nodeTransition{Ref: ref, Actor: actor, To: runstate.NodeSuccess, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(row.Attempt)}
			for replay := range 2 {
				applied, err := tenant.TransitionNode(t.Context(), change)
				if err != nil || applied.Replayed != (replay == 1) {
					t.Fatalf("real calculator settlement: %+v: %v", applied, err)
				}
				unitBillingTotal(t, s, 60*wantPrice, true)
			}
		})
	}
}

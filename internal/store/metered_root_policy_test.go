package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestMeteredRootPolicyCannotRewriteAwardProperty(t *testing.T) {
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			cloud, metered := i&1 != 0, i&2 != 0
			s, demand, now := meteredUnitFixture(t, cloud)
			principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
			if err != nil {
				t.Fatal(err)
			}
			root := principal.Capability
			setMarker := func(value bool) {
				t.Helper()
				root.RootMetered = value
				body, err := json.Marshal(CapabilityRecord{Version: 1, Value: root})
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, s.db, "UPDATE capabilities SET authority_json=?,metered=? WHERE prefix=?", body, boolInt(value), root.Prefix)
			}
			setMarker(metered)
			claim, actor := meteredUnitClaim(t, s, demand)
			award, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
			if err != nil {
				t.Fatal(err)
			}
			wantMetered := metered || cloud
			if award.Capability.Billing == nil || award.Capability.Billing.Metered != wantMetered {
				t.Fatalf("award metering: %+v; want %t", award.Capability.Billing, wantMetered)
			}
			var persisted bool
			if err := s.db.QueryRow("SELECT metered FROM billing_awards WHERE reservation_id=?", award.Capability.Billing.ReservationID).Scan(&persisted); err != nil || persisted != wantMetered {
				t.Fatalf("persisted metering=%t, %v; want %t", persisted, err, wantMetered)
			}
			setMarker(!metered)
			tenant := &Tenant{s: s, team: "a"}
			admitExecution(t, tenant, actor)
			started, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID)}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)})
			if err != nil {
				t.Fatal(err)
			}
			*now = now.Add(time.Duration(i+1) * time.Second)
			change := nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID)}, Actor: actor, To: runstate.NodeSuccess, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(int64(started.Attempt))}
			for replay := range 2 {
				applied, err := tenant.TransitionNode(t.Context(), change)
				if err != nil || applied.Replayed != (replay == 1) {
					t.Fatalf("settlement: %+v, %v", applied, err)
				}
				want := int64(0)
				if wantMetered {
					want = 600
				}
				unitBillingTotal(t, s, want, true)
			}
		})
	}
}

package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func priorCloudAward(t *testing.T, s *Store, at time.Time) {
	t.Helper()
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=1 WHERE run_id='run' AND node_id='job'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at) VALUES ('a','prior-award','prior-cloud','run','job',1,'a',1,'cloud','at_start','node','job',?)`, at.UnixNano())
}

// A Cloud job that never started within its standing duration limit fails once
// with the reason, instead of refusing every later claim while its run stays running.
func TestExpiredCloudJobFailsOnceInsteadOfRefusingEveryClaim(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("queued=%t", queued), func(t *testing.T) {
			s, demand, now := meteredUnitFixture(t, true)
			priorCloudAward(t, s, now.Add(-4*time.Hour-10*time.Minute))
			if queued {
				demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitNode}
			}
			if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired Cloud job claim: %v", err)
			}
			_, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			var expired *JobDurationLimitError
			if err == nil || errors.As(err, &expired) || queued && !errors.Is(err, ErrNotFound) {
				t.Fatalf("claim after the failure: %v", err)
			}
			row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeFailed || row.FailureReason != FailureJobDurationLimit || row.ClaimedBy != "" || row.Attempt != 0 {
				t.Fatalf("expired Cloud job was not failed: %+v: %v", row, err)
			}
			var message string
			if err := s.db.QueryRowContext(t.Context(), `SELECT error FROM nodes WHERE run_id='run' AND node_id='job'`).Scan(&message); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"never started", "standing job duration limit of 4h0m0s", "first awarded 4h10m0s ago", "Cloud capacity"} {
				if !strings.Contains(message, want) {
					t.Fatalf("failure %q omits %q", message, want)
				}
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 1)
		})
	}
}

func TestCloudJobWithinItsDurationLimitIsStillAwarded(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	priorCloudAward(t, s, now.Add(-3*time.Hour))
	if claim, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil || claim.Award.CapabilityPrefix == "" {
		t.Fatalf("Cloud job within its limit was not awarded: %+v %v", claim, err)
	}
}

func TestDurationLimitFailureNeedsAnExpiredJobAndItsLauncher(t *testing.T) {
	for _, variant := range []string{"expired", "public caller", "within limit", "foreign claimant", "wrong reason"} {
		t.Run(variant, func(t *testing.T) {
			s, demand, now := meteredUnitFixture(t, true)
			priorCloudAward(t, s, now.Add(-4*time.Hour-time.Minute))
			actor := runstate.Actor{Kind: runstate.ActorClaimant, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}, CapabilityPrefix: demand.CapabilityPrefix}
			reason, proof := FailureJobDurationLimit, demand.CapabilityPrefix
			switch variant {
			case "public caller":
				proof = ""
			case "within limit":
				rewriteSchemaExec(t, s.db, `UPDATE billing_awards SET awarded_at=?`, now.Add(-time.Hour).UnixNano())
			case "foreign claimant":
				other := authorityRoot("self-hosted", "a")
				other.Grants = []capability.Grant{capability.NodesClaim}
				authorityPersist(t, s, other)
				actor.Authority.ID, actor.CapabilityPrefix, proof = other.Prefix, other.Prefix, other.Prefix
			case "wrong reason":
				reason = FailureUnpricedCPUClass
			}
			change := nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: runstate.NodeFailed, Cause: runstate.CauseDurationLimit, Actor: actor, Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: reason}}}
			err := s.inTx(t.Context(), func(tx *storeTx) error {
				tx.expiredClaimant = proof
				_, err := transitionNodeTx(t.Context(), tx, "a", change)
				return err
			})
			if accepted := variant == "expired"; (err == nil) != accepted {
				t.Fatalf("accepted=%t: %v", accepted, err)
			}
			row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), change.Ref)
			if err != nil || (row.Outcome == runstate.NodeFailed) != (variant == "expired") {
				t.Fatalf("job after %s failure: %+v %v", variant, row, err)
			}
		})
	}
}

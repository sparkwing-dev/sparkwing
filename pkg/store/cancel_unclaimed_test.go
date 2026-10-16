package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func cancellationFixture(t *testing.T, kind capability.UnitKind) (*Store, *Tenant, UnitDemand, RunTransition) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	fundUnitClaim(t, s)
	rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET status='pending'`)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind=?`, kind)
	demand.Target = &UnitRef{Kind: kind, RunID: "run"}
	if kind == capability.UnitPlan {
		root := authorityRoot("launcher", "")
		root.Subject.Kind = capability.SubjectService
		root.Grants = []capability.Grant{capability.ClaimsLaunch}
		authorityPersist(t, s, root)
		demand.CapabilityPrefix = root.Prefix
		rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
	}
	actor := authorityMember(t, s, "operator", RoleOwner)
	actor.Kind = ActorOperator
	return s, &Tenant{s: s, team: "a"}, demand, RunTransition{RunID: "run", To: RunCancelled, Cause: RunCancel, Actor: actor, Reason: "user_cancelled"}
}

func TestRunCancellationPreventsUnclaimedAwards(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan} {
		t.Run(string(kind), func(t *testing.T) {
			s, tenant, demand, change := cancellationFixture(t, kind)
			if _, err := tenant.TransitionRun(t.Context(), change); err != nil {
				t.Fatal(err)
			}
			for _, named := range []bool{true, false} {
				if !named {
					demand.Kinds, demand.Target = []capability.UnitKind{kind}, nil
				}
				if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
					t.Fatal("cancelled owner unit was awarded")
				}
			}
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			for replay := range 2 {
				units, err := s.ReapUnits(t.Context(), lease, 10)
				if err != nil || len(units) != 1-replay || replay == 0 && !units[0].Cancelled {
					t.Fatalf("cancel recovery: %+v, %v", units, err)
				}
			}
			row, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || row.State != RunCancelled || row.FinishedAt == nil || row.CancelReason != change.Reason {
				t.Fatalf("unclaimed cancellation: %+v, %v", row, err)
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 0)
			rewriteSchemaCount(t, s.db, "node_attempts", 0)
		})
	}
}

func TestRunCancellationRacesOwnerAwardProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(506, 82))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			kind := capability.UnitTrigger
			s, tenant, demand, change := cancellationFixture(t, kind)
			if random.IntN(2) == 0 {
				demand.Kinds, demand.Target = []capability.UnitKind{kind}, nil
			}
			change.Reason = fmt.Sprintf("cancel/'%x", random.Uint64())
			start := make(chan struct{})
			var wg sync.WaitGroup
			var claim UnitClaim
			var claimErr, cancelErr error
			wg.Go(func() {
				<-start
				claim, claimErr = s.ClaimUnit(t.Context(), demand)
			})
			wg.Go(func() {
				<-start
				_, cancelErr = tenant.TransitionRun(t.Context(), change)
			})
			close(start)
			wg.Wait()
			if cancelErr != nil {
				t.Fatal(cancelErr)
			}
			row, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || row.CancelRequestedAt == nil || row.CancelReason != change.Reason || row.FinishedAt != nil {
				t.Fatalf("cancellation under holder: %+v, %v", row, err)
			}
			if claimErr == nil {
				beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false)
				if err != nil || !beat.CancelRequested || beat.CancelReason != change.Reason {
					t.Fatalf("awarded owner missed cancellation: %+v, %v", beat, err)
				}
			} else {
				var denied *apierr.Error
				var illegal *ErrIllegalTransition
				if !errors.Is(claimErr, ErrNotFound) && (!errors.As(claimErr, &denied) || denied.Code != apierr.LockHeld && denied.Code != apierr.Forbidden) && (!errors.As(claimErr, &illegal) || illegal.Reason != ReasonHolderRights) {
					t.Fatal(claimErr)
				}
				rewriteSchemaCount(t, s.db, "billing_awards", 0)
			}
		})
	}
}

func TestRunCancellationReachesClaimedOwner(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan} {
		t.Run(string(kind), func(t *testing.T) {
			s, tenant, demand, change := cancellationFixture(t, kind)
			claim, err := s.ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			prefix := claim.Award.CapabilityPrefix
			if kind == capability.UnitPlan {
				issued, err := s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: prefix})
				if err != nil {
					t.Fatal(err)
				}
				prefix = issued.Capability.Prefix
			}
			if _, err := tenant.TransitionRun(t.Context(), change); err != nil {
				t.Fatal(err)
			}
			beat, err := s.RenewUnit(t.Context(), prefix, false)
			if err != nil || !beat.CancelRequested || beat.CancelReason != change.Reason {
				t.Fatalf("held cancellation: %+v, %v", beat, err)
			}
			row, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || row.FinishedAt != nil {
				t.Fatalf("cancellation ended a held run: %+v, %v", row, err)
			}
		})
	}
}

func TestRunCancellationRequiresCurrentTeamAuthorityProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(507, 19))
	for _, problem := range []string{"tenant", "membership", "role", "session", "expiry", "revocation", "unknown"} {
		for i := range 4 {
			t.Run(fmt.Sprintf("%s/%d", problem, i), func(t *testing.T) {
				s, tenant, _, change := cancellationFixture(t, capability.UnitTrigger)
				before, err := tenant.ReadRun(t.Context(), "run")
				if err != nil {
					t.Fatal(err)
				}
				writer := *tenant
				switch problem {
				case "tenant":
					writer.team = "b"
				case "membership":
					rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE account_id='operator'`)
				case "role":
					rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='reader' WHERE account_id='operator'`)
				case "session":
					rewriteSchemaExec(t, s.db, `DELETE FROM sessions WHERE hash='operator'`)
				case "expiry":
					rewriteSchemaExec(t, s.db, `UPDATE sessions SET expires_at=?`, s.now().Add(-time.Duration(random.IntN(1000))).UnixNano())
				case "revocation":
					rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=?`, s.now().Add(-time.Duration(random.IntN(1000))).UnixNano())
				case "unknown":
					change.Actor.Authority.ID = fmt.Sprintf("missing/'%x", random.Uint64())
				}
				if _, err := writer.TransitionRun(t.Context(), change); err == nil {
					t.Fatal("unavailable authority cancelled a run")
				}
				after, err := tenant.ReadRun(t.Context(), "run")
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("refused cancellation changed run: %+v, %v", after, err)
				}
				rewriteSchemaCount(t, s.db, "events", 0)
				rewriteSchemaCount(t, s.db, "run_effects", 0)
			})
		}
	}
}

func TestRunCancellationCannotOverwriteTerminalProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(52, 361))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, tenant, _, change := cancellationFixture(t, capability.UnitTrigger)
			state := []RunState{RunSuccess, RunFailed, RunCancelled}[random.IntN(3)]
			rewriteSchemaExec(t, s.db, `UPDATE runs SET status=?,finished_at=?`, state, s.now().UnixNano())
			before, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			var illegal *ErrIllegalTransition
			if _, err := tenant.TransitionRun(t.Context(), change); !errors.As(err, &illegal) || illegal.Reason != ReasonTerminal {
				t.Fatalf("terminal cancellation: %v", err)
			}
			after, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || after.State != before.State || after.StateSeq != before.StateSeq || !after.FinishedAt.Equal(*before.FinishedAt) || after.CancelRequestedAt != nil {
				t.Fatalf("terminal cancellation mutated outcome: %+v, %v", after, err)
			}
			rewriteSchemaCount(t, s.db, "events", 0)
		})
	}
}

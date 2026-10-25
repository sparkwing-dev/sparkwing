package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestOwnerReapersLockTeamBeforeOwner(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, recorder := newRecordingStore(t)
			rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
			unitClaimFixtureStore(t, s)
			table := "triggers"
			if kind == capability.UnitEval {
				s.TestOnlyEvaluations(t, "job")
				table = "eval_units"
			} else {
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind=?`, kind)
			}
			rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET status='claimed',claimed_by='lost',capability_prefix='lost-cap',generation=7,claimed_at=1,lease_expires_at=1`)
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			recorder.reset()
			reaped, err := s.AsOperator().ReapUnits(t.Context(), lease, 1)
			if err != nil || len(reaped) != 1 || !reaped[0].Requeued {
				t.Fatalf("reap: %+v, %v", reaped, err)
			}
			team, owner := -1, -1
			for i, q := range recorder.snapshot() {
				if q == "SELECT name FROM teams WHERE name=?" && team < 0 {
					team = i
				}
				if strings.Contains(q, "FROM "+table+" WHERE team=?") && owner < 0 {
					owner = i
				}
			}
			if team < 0 || owner <= team {
				t.Fatalf("team lock=%d, owner=%d; team must precede owner", team, owner)
			}
		})
	}
}

func TestConcurrencyMaintenanceLocksTeamBeforeCapacity(t *testing.T) {
	s, recorder := newRecordingStore(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
	maintenanceRun(t, s, "run", 1, runstate.RunRunning)
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a','job:global:key',1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,arrived_at,lease_expires_at,policy) VALUES ('a','job:global:key','expired','run',1,1,'queue')`)
	lease := maintenanceReaper(t, s)
	recorder.reset()
	result, err := s.AsOperator().MaintainConcurrency(t.Context(), lease)
	if err != nil || len(result.StaleWaiters) != 1 {
		t.Fatalf("maintenance: %+v, %v", result, err)
	}
	team, entry := -1, -1
	for i, q := range recorder.snapshot() {
		if q == "SELECT name FROM teams WHERE name=?" && team < 0 {
			team = i
		}
		if strings.HasPrefix(q, "SELECT key FROM concurrency_entries WHERE team = ? AND key = ?") && entry < 0 {
			entry = i
		}
	}
	if team < 0 || entry <= team {
		t.Fatalf("team lock=%d, capacity lock=%d; team must precede capacity", team, entry)
	}
}

func TestDirectGrantLocksTeamBeforeWritingLedger(t *testing.T) {
	s, recorder := newRecordingStore(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('operator','operator@example.test',1,1)`)
	recorder.reset()
	if result, err := (&Tenant{s: s, team: "a"}).RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1, Reference: "first", CreatedBy: "operator"}); err != nil || !result.Created {
		t.Fatalf("grant=%+v: %v", result, err)
	}
	team, grant := -1, -1
	for i, q := range recorder.snapshot() {
		if strings.HasPrefix(q, "SELECT name FROM teams WHERE name=?") {
			team = i
		}
		if strings.HasPrefix(q, "INSERT INTO credit_grants") {
			grant = i
			break
		}
	}
	if team < 0 || grant <= team {
		t.Fatalf("team lock=%d, grant=%d; team must precede ledger", team, grant)
	}
}

func TestFinancialLockOrderRejectsUpgradesAndLateTeams(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "late_team"}[late], func(t *testing.T) {
			s := authorityStore(t)
			err := s.inTx(t.Context(), func(tx *storeTx) error {
				if late {
					if _, err := lockLedgerTx(t.Context(), tx, "a"); err != nil {
						return err
					}
					_, _ = lockLedgerTx(t.Context(), tx, "b")
				} else {
					if err := acquireLockSet(t.Context(), tx, func() lockSet { var set lockSet; set.add(lockTeam, false, "a"); return set }); err != nil {
						return err
					}
					_, _ = lockLedgerTx(t.Context(), tx, "a")
				}
				return nil
			})
			if err == nil {
				t.Fatal("financial lock inversion committed")
			}
		})
	}
	if compareLockTiers(lockTeam, lockLedger) >= 0 {
		t.Fatal("team locks must precede ledger locks")
	}
	for _, work := range []storeLockTier{lockTrigger, lockEval, lockRun, lockNode} {
		if compareLockTiers(lockTeam, work) >= 0 || compareLockTiers(work, lockLedger) >= 0 {
			t.Fatalf("%s locks must follow teams and precede ledgers", work)
		}
	}
}

func TestPaymentModeFreezeRollsBackWithFinancialWrite(t *testing.T) {
	s := authorityStore(t)
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		if _, err := recordCreditGrantTx(t.Context(), tx, "a", CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1, Reference: "first"}, auditActor{system: auditBreakGlass}); err != nil {
			return err
		}
		return context.Canceled
	})
	if err != context.Canceled {
		t.Fatalf("rollback: %v", err)
	}
	if err := s.AsOperator().setPaymentMode(t.Context(), "live"); err != nil {
		t.Fatalf("rolled-back activity froze the mode: %v", err)
	}
}

func TestWarningLocksSavedCardTeamsBeforeClearingCards(t *testing.T) {
	s, recorder := newRecordingStore(t)
	if err := s.AsOperator().setPaymentMode(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at,card_customer,card_payment_method,card_fingerprint,refill_enabled,refill_amount_cents) VALUES ('z',1,1,'customer','method','card',1,1000),('a',1,1,'customer','method','card',1,1000)`)
	recorder.reset()
	if _, err := s.settlePaymentFacts(t.Context(), capability.PaymentEvent{Mode: capability.PaymentTest, EventID: "warning", ObjectID: "warning", PaymentID: "payment", Kind: capability.PaymentWarningEvent, Fingerprint: "card", Actionable: true, Created: 1}); err != nil {
		t.Fatal(err)
	}
	var teams []string
	for _, call := range recorder.snapshotCalls() {
		if strings.HasPrefix(call.statement, "UPDATE teams SET card_customer") {
			break
		}
		if call.statement == "SELECT name FROM teams WHERE name=?" {
			teams = append(teams, fmt.Sprint(call.args[0].Value))
		}
	}
	if !slices.Equal(teams, []string{"a", "z"}) {
		t.Fatalf("team locks before card clearing=%v, want [a z]", teams)
	}
	rewriteSchemaCount(t, s.db, "teams WHERE card_fingerprint<>'' OR refill_enabled<>0", 0)
}

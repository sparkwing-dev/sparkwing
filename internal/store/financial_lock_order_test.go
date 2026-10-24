package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestDirectGrantLocksTeamBeforeWritingLedger(t *testing.T) {
	s, recorder := newRecordingStore(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`)
	recorder.reset()
	if _, err := (&Tenant{s: s, team: "a"}).RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1, Reference: "first"}); err != nil {
		t.Fatal(err)
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
	if err := s.AsOperator().SetPaymentMode(t.Context(), "live"); err != nil {
		t.Fatalf("rolled-back activity froze the mode: %v", err)
	}
}

func TestWarningLocksSavedCardTeamsBeforeClearingCards(t *testing.T) {
	s, recorder := newRecordingStore(t)
	if err := s.AsOperator().SetPaymentMode(t.Context(), "test"); err != nil {
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

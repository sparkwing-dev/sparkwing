package store

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentCardIdentityCannotFundTwoTeams(t *testing.T) {
	for _, kinds := range [][2]string{{"card", "card"}, {"checkout", "card"}, {"card", "checkout"}, {"checkout", "checkout"}} {
		t.Run(kinds[0]+"_to_"+kinds[1], func(t *testing.T) {
			s, event := paymentFixture(t, capability.PaymentTest)
			now := s.now()
			rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('other','b','other-session','test','usd',100,1000000,1,2)`)
			for _, team := range []Team{"a", "b"} {
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,seconds,kind,amount_micro,charged_at) VALUES (?,?,'','','',1,'usage',1000000,1)`, "spent-"+string(team), team)
				rewriteSchemaExec(t, s.db, `INSERT INTO card_charges(id,team,amount_micro,cause,state,next_attempt_at,opened_at) VALUES (?,?,1000000,'rung','open',1,1)`, "charge-"+string(team), team)
				rewriteSchemaExec(t, s.db, `INSERT INTO card_attempts(id,team,charge_id,kind,status,created_at,updated_at) VALUES (?,?,?,'off_session','live',1,1)`, "attempt-"+string(team), team, "charge-"+string(team))
			}
			settle := func(kind string, team Team) (bool, error) {
				switch kind {
				case "card":
					created, _, err := s.SettleCardPayment(t.Context(), CardPayment{Team: team, ChargeID: "charge-" + string(team), AttemptID: "attempt-" + string(team), PaymentIntent: event.PaymentID, AmountCents: 100}, now)
					return created, err
				default:
					facts := event
					if team == "b" {
						facts.CheckoutID, facts.EventID = "other-session", "other-event"
					}
					result, err := s.settlePaymentFacts(t.Context(), facts)
					return err == nil && !result.Replayed, err
				}
			}
			if created, err := settle(kinds[0], "a"); err != nil || !created {
				t.Fatalf("first payment=%v: %v", created, err)
			}
			if created, err := settle(kinds[0], "a"); err != nil || created {
				t.Fatalf("payment replay=%v: %v", created, err)
			}
			type ledger struct {
				balance int64
				grants  []CreditGrant
			}
			snapshot := func() map[Team]ledger {
				t.Helper()
				out := map[Team]ledger{}
				for _, team := range []Team{"a", "b"} {
					tenant := &Tenant{s: s, team: team}
					balance, err := tenant.CreditBalanceMicro(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					grants, err := tenant.ListCreditGrants(t.Context(), 50)
					if err != nil {
						t.Fatal(err)
					}
					out[team] = ledger{balance, grants}
				}
				return out
			}
			before := snapshot()
			if created, err := settle(kinds[1], "b"); created || !errors.Is(err, ErrCreditGrantConflict) {
				t.Fatalf("cross-team payment=%v: %v", created, err)
			}
			if after := snapshot(); !reflect.DeepEqual(before, after) {
				t.Fatalf("refused payment moved ledger: before=%+v, after=%+v", before, after)
			}
		})
	}
}

func TestPaymentRefundCanTakeSpentBalanceBelowZero(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,seconds,kind,amount_micro,charged_at) VALUES ('spent','a','','','',1,'usage',700000,1)`)
	got, err := s.ReversePayment(t.Context(), event.PaymentID, "refund", "console", 0)
	if err != nil || got.Grant == nil || got.Grant.AmountMicro != -1000000 || got.BalanceMicro != -700000 {
		t.Fatalf("spent payment refund=%+v: %v", got, err)
	}
}

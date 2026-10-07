package store_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestCardPaymentReferenceCannotFundTwoTeams(t *testing.T) {
	for _, tc := range []struct{ name, first, second string }{
		{"card to card", "card", "card"}, {"grant to card", "grant", "card"}, {"card to grant", "card", "grant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.Open(t)
			ctx, now := t.Context(), time.Now()
			for _, name := range []string{"acme", "globex"} {
				team := teamHandle(t, s, store.Team(name))
				trustWithCard(t, team, now)
				spend(t, s, name, "charge-"+name, 15_000)
			}
			work, _, err := s.DueCardCharges(ctx, now)
			if err != nil || len(work) != 2 {
				t.Fatalf("due charges = %d, %v; want two", len(work), err)
			}
			charges := map[store.Team]store.CardChargeWork{}
			for _, item := range work {
				charges[item.Team] = item
			}
			settle := func(kind string, p store.CardPayment) (bool, error) {
				if kind == "card" {
					created, _, err := s.SettleCardPayment(ctx, p, now)
					return created, err
				}
				team, err := s.ForTeam(ctx, p.Team)
				if err != nil {
					return false, err
				}
				result, err := team.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: p.AmountCents * store.MicroCreditsPerCent, Reference: p.PaymentIntent})
				return result.Created, err
			}
			first := charges["acme"]
			payment := store.CardPayment{
				Team: first.Team, ChargeID: first.ChargeID, AttemptID: first.AttemptID,
				PaymentIntent: "pi_private_fixture", AmountCents: first.AmountCents,
			}
			if created, err := settle(tc.first, payment); err != nil || !created {
				t.Fatalf("first settlement = %v, %v", created, err)
			}
			if created, err := settle(tc.first, payment); err != nil || created {
				t.Fatalf("same-team replay = %v, %v; want no new grant", created, err)
			}
			type state struct {
				balance int64
				grants  []store.CreditGrant
			}
			snapshot := func() map[store.Team]state {
				t.Helper()
				result := map[store.Team]state{}
				for name := range charges {
					team, err := s.ForTeam(ctx, name)
					if err != nil {
						t.Fatal(err)
					}
					balance, err := team.CreditBalanceMicro(ctx)
					if err != nil {
						t.Fatal(err)
					}
					grants, err := team.ListCreditGrants(ctx, 50)
					if err != nil {
						t.Fatal(err)
					}
					result[name] = state{balance, grants}
				}
				return result
			}
			before := snapshot()
			second := charges["globex"]
			payment.Team, payment.ChargeID, payment.AttemptID = second.Team, second.ChargeID, second.AttemptID
			created, settleErr := settle(tc.second, payment)
			if !errors.Is(settleErr, store.ErrCreditGrantConflict) || created {
				t.Fatalf("cross-team payment replay: created=%v err=%v; want conflict", created, settleErr)
			}
			if !reflect.DeepEqual(before, snapshot()) {
				t.Fatal("refused payment replay changed team balances or grant history")
			}
		})
	}
}

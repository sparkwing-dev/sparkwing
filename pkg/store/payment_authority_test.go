package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func FuzzPaymentRawPaidGrantsCannotFundTeam(f *testing.F) {
	f.Add("provider-session", "billing", uint32(100), false)
	f.Add("payment:test:checkout", "operator", uint32(50000), true)
	f.Add("", "", uint32(0), false)
	f.Fuzz(func(t *testing.T, reference, actor string, cents uint32, other bool) {
		s, event := paymentFixture(t, capability.PaymentTest)
		rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)", DefaultTeam)
		team := Team("a")
		if other {
			team = "b"
		}
		tenant := &Tenant{s: s, team: team}
		amount := (int64(cents)%CreditPurchaseMaxCents + 1) * MicroCreditsPerCent
		grants := []func() error{
			func() error {
				_, err := tenant.GrantCredits(t.Context(), CreditGrantPaid, amount, reference, actor)
				return err
			},
			func() error {
				_, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantPaid, AmountMicro: amount, Reference: reference, CreatedBy: actor})
				return err
			},
		}
		refuse := func() {
			t.Helper()
			for _, grant := range grants {
				if err := grant(); err == nil {
					t.Fatal("caller-selected paid credit was accepted")
				}
			}
		}
		refuse()
		rewriteSchemaCount(t, s.db, "credit_grants", 0)
		rewriteSchemaCount(t, s.db, "payment_events", 0)
		for _, selected := range []Team{"a", "b", DefaultTeam} {
			balance, err := (&Tenant{s: s, team: selected}).CreditBalanceMicro(t.Context())
			if err != nil || balance != 0 {
				t.Fatalf("raw paid grant changed %s balance: %d, %v", selected, balance, err)
			}
		}
		paid, err := s.settlePaymentFacts(t.Context(), event)
		if err != nil || paid.Team != "a" || paid.AmountMicro != 1000000 || paid.Replayed {
			t.Fatalf("verified checkout after raw refusal: %+v, %v", paid, err)
		}
		refuse()
		rewriteSchemaCount(t, s.db, "credit_grants", 1)
	})
}

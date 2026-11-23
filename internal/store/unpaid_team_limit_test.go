package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func createOwnedTeam(t *testing.T, st *store.Store, accountID string, slug store.Team) error {
	t.Helper()
	_, err := st.AsOperator().CreateAccountTeam(context.Background(), storetest.AccountAuthority(t, st, accountID), store.TeamInfo{Slug: slug})
	return err
}

func requireUnpaidTeamLimit(t *testing.T, err error) {
	t.Helper()
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != hosterr.LimitExceeded || failure.Details["reason"] != "unpaid_team_limit" {
		t.Fatalf("team creation past the unpaid limit = %v, want limit_exceeded with reason unpaid_team_limit", err)
	}
	for _, want := range []string{"unpaid team limit is 3 per account", "you own 3 teams that have never paid", "add funds", "support@sparkwing.dev"} {
		if !strings.Contains(failure.Message, want) {
			t.Fatalf("refusal %q omits %q", failure.Message, want)
		}
	}
}

func payTeam(t *testing.T, st *store.Store, team store.Team, reference string) {
	t.Helper()
	if _, err := tenant(t, st, team).TestOnlySettlePaidCredit(context.Background(), store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 500 * store.MicroCreditsPerCent, Reference: reference, CreatedBy: "billing"}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountOwnsAtMostThreeUnpaidTeams(t *testing.T) {
	st := storetest.Open(t)
	u := signIn(t, st, "s", "busy@example.com")
	for _, slug := range []store.Team{"busy-two", "busy-three"} {
		if err := createOwnedTeam(t, st, u.Account.ID, slug); err != nil {
			t.Fatalf("team %s: %v", slug, err)
		}
	}
	requireUnpaidTeamLimit(t, createOwnedTeam(t, st, u.Account.ID, "busy-four"))
	if _, err := st.TeamInfo(context.Background(), "busy-four"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused team was registered: %v", err)
	}
	other := signIn(t, st, "o", "other@example.com")
	if err := createOwnedTeam(t, st, other.Account.ID, "other-two"); err != nil {
		t.Fatalf("another account's team: %v", err)
	}
}

func TestPaidTeamsDoNotCountTowardTheUnpaidTeamLimit(t *testing.T) {
	st := storetest.Open(t)
	u := signIn(t, st, "s", "payer@example.com")
	payTeam(t, st, u.PersonalTeam, "pay-personal")
	for _, slug := range []store.Team{"payer-two", "payer-three", "payer-four"} {
		if err := createOwnedTeam(t, st, u.Account.ID, slug); err != nil {
			t.Fatalf("team %s beside a paid team: %v", slug, err)
		}
	}
	requireUnpaidTeamLimit(t, createOwnedTeam(t, st, u.Account.ID, "payer-five"))
}

func TestFullyRefundedTeamCountsAsUnpaid(t *testing.T) {
	st := storetest.Open(t)
	u := signIn(t, st, "s", "refund@example.com")
	payTeam(t, st, u.PersonalTeam, "pay-refunded")
	for _, slug := range []store.Team{"refund-two", "refund-three"} {
		if err := createOwnedTeam(t, st, u.Account.ID, slug); err != nil {
			t.Fatalf("team %s: %v", slug, err)
		}
	}
	if _, err := st.AsOperator().TestOnlyReversePayment(context.Background(), "pay-refunded", "refund-pay-refunded", "billing", 0); err != nil {
		t.Fatal(err)
	}
	requireUnpaidTeamLimit(t, createOwnedTeam(t, st, u.Account.ID, "refund-four"))
}

func TestConcurrentTeamCreationKeepsTheUnpaidTeamLimit(t *testing.T) {
	for name, open := range map[string]func(*testing.T) *store.Store{
		"sqlite":   func(t *testing.T) *store.Store { return storetest.OpenSQLite(t) },
		"postgres": storetest.OpenPostgres,
	} {
		t.Run(name, func(t *testing.T) {
			st := open(t)
			u := signIn(t, st, "s", "racer@example.com")
			const attempts = 8
			results := make(chan error, attempts)
			for i := range attempts {
				go func() {
					_, err := st.AsOperator().CreateAccountTeam(context.Background(), storetest.AccountAuthority(t, st, u.Account.ID), store.TeamInfo{Slug: store.Team(fmt.Sprintf("racer-%d", i))})
					results <- err
				}()
			}
			created := 0
			for range attempts {
				err := <-results
				if err == nil {
					created++
					continue
				}
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != hosterr.LimitExceeded {
					t.Fatalf("concurrent creation: %v", err)
				}
			}
			if created != 2 {
				t.Fatalf("created %d teams beside the personal team, want 2", created)
			}
			memberships, err := st.AsOperator().AccountMemberships(context.Background(), u.Account.ID)
			if err != nil || len(memberships) != 3 {
				t.Fatalf("account owns %d teams, want 3: %v", len(memberships), err)
			}
		})
	}
}

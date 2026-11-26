package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func mustCreateOwnedTeam(t *testing.T, st *store.Store, accountID string, slug store.Team) {
	t.Helper()
	if err := createOwnedTeam(t, st, accountID, slug); err != nil {
		t.Fatalf("create team %s: %v", slug, err)
	}
}

func inviteAt(t *testing.T, st *store.Store, accountID string, team store.Team, to string, at time.Time) store.Invitation {
	t.Helper()
	st.SetTestClock(t, func() time.Time { return at })
	inv, err := tenant(t, st, team).InviteForTest(t, context.Background(), accountID, to, store.RoleReader, at)
	if err != nil {
		t.Fatalf("invite %s from %s: %v", to, team, err)
	}
	return inv
}

func claimAt(t *testing.T, st *store.Store, inv store.Invitation, at time.Time) (bool, error) {
	withheld, err := claimReasonAt(t, st, inv, at)
	return err == nil && withheld == "", err
}

func claimReasonAt(t *testing.T, st *store.Store, inv store.Invitation, at time.Time) (store.InvitationEmailWithheld, error) {
	t.Helper()
	st.SetTestClock(t, func() time.Time { return at })
	return st.AsOperator().ClaimInvitationEmail(context.Background(), inv.ID)
}

// safety: each email goes to its own address, so the per-recipient cap never refuses one.
func sendInvitationEmails(t *testing.T, st *store.Store, accountID string, teams []store.Team, prefix string, n int, at time.Time) {
	t.Helper()
	for i := range n {
		inv := inviteAt(t, st, accountID, teams[i%len(teams)], fmt.Sprintf("%s-%d@example.com", prefix, i), at)
		if ok, err := claimAt(t, st, inv, at); err != nil || !ok {
			t.Fatalf("invitation email %d from %s = %v, %v; want sent", i+1, accountID, ok, err)
		}
	}
}

func wantAccountLimit(t *testing.T, ok bool, err error, firstSent time.Time) {
	t.Helper()
	var limit *store.InvitationEmailLimitError
	if ok || !errors.As(err, &limit) {
		t.Fatalf("invitation email past the account cap = %v, %v; want the account limit", ok, err)
	}
	if earliest := firstSent.Add(24 * time.Hour); limit.RetryAt.Before(earliest) || limit.RetryAt.After(earliest.Add(time.Minute)) {
		t.Fatalf("retry at %s, want 24 hours after the first email at %s", limit.RetryAt, firstSent)
	}
}

func TestAccountSendsAtMostOneHundredInvitationEmailsADayAcrossItsTeams(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	teams := []store.Team{owner.PersonalTeam, "second", "third"}
	mustCreateOwnedTeam(t, st, owner.Account.ID, teams[1])
	mustCreateOwnedTeam(t, st, owner.Account.ID, teams[2])
	sendInvitationEmails(t, st, owner.Account.ID, teams, "guest", store.MaxInvitationEmailsPerAccountPerDay, now)
	for i, team := range teams {
		inv := inviteAt(t, st, owner.Account.ID, team, fmt.Sprintf("late-%d@example.com", i), now)
		ok, err := claimAt(t, st, inv, now)
		wantAccountLimit(t, ok, err, now)
	}
}

func TestDeletingTeamsDoesNotResetTheAccountInvitationEmailCap(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	for round, n := range []int{40, 40, 20} {
		slug := store.Team(fmt.Sprintf("churn-%d", round))
		mustCreateOwnedTeam(t, st, owner.Account.ID, slug)
		sendInvitationEmails(t, st, owner.Account.ID, []store.Team{slug}, string(slug), n, now)
		deleteAndPurge(t, st, slug, now)
	}
	mustCreateOwnedTeam(t, st, owner.Account.ID, "fresh")
	inv := inviteAt(t, st, owner.Account.ID, "fresh", "after-churn@example.com", now)
	ok, err := claimAt(t, st, inv, now)
	wantAccountLimit(t, ok, err, now)
}

func TestAccountInvitationEmailCapRollsOverAfterADay(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	teams := []store.Team{owner.PersonalTeam, "second", "third"}
	mustCreateOwnedTeam(t, st, owner.Account.ID, teams[1])
	mustCreateOwnedTeam(t, st, owner.Account.ID, teams[2])
	sendInvitationEmails(t, st, owner.Account.ID, teams, "guest", store.MaxInvitationEmailsPerAccountPerDay, now)

	almost := now.Add(23 * time.Hour)
	inv := inviteAt(t, st, owner.Account.ID, teams[0], "almost@example.com", almost)
	ok, err := claimAt(t, st, inv, almost)
	wantAccountLimit(t, ok, err, now)

	later := now.Add(24*time.Hour + time.Second)
	inv = inviteAt(t, st, owner.Account.ID, teams[1], "next-day@example.com", later)
	if ok, err := claimAt(t, st, inv, later); err != nil || !ok {
		t.Fatalf("invitation email a day later = %v, %v; want sent", ok, err)
	}
}

func TestAccountInvitationEmailCapsAreIndependent(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	busy := signIn(t, st, "b", "busy@example.com")
	quiet := signIn(t, st, "q", "quiet@example.com")
	teams := []store.Team{busy.PersonalTeam, "busy-second", "busy-third"}
	mustCreateOwnedTeam(t, st, busy.Account.ID, teams[1])
	mustCreateOwnedTeam(t, st, busy.Account.ID, teams[2])
	sendInvitationEmails(t, st, busy.Account.ID, teams, "busy", store.MaxInvitationEmailsPerAccountPerDay, now)

	inv := inviteAt(t, st, quiet.Account.ID, quiet.PersonalTeam, "quiet-guest@example.com", now)
	if ok, err := claimAt(t, st, inv, now); err != nil || !ok {
		t.Fatalf("another account's first invitation email = %v, %v; want sent", ok, err)
	}
	inv = inviteAt(t, st, busy.Account.ID, teams[0], "busy-late@example.com", now)
	ok, err := claimAt(t, st, inv, now)
	wantAccountLimit(t, ok, err, now)
}

func TestConcurrentInvitationEmailsShareTheAccountsLastSlot(t *testing.T) {
	for name, open := range map[string]func(*testing.T) *store.Store{
		"sqlite":   func(t *testing.T) *store.Store { return storetest.OpenSQLite(t) },
		"postgres": storetest.OpenPostgres,
	} {
		t.Run(name, func(t *testing.T) {
			st := open(t)
			const racers = 16
			st.DB().SetMaxOpenConns(racers)
			now := time.Now()
			for round := range 5 {
				owner := signIn(t, st, fmt.Sprintf("o%d", round), fmt.Sprintf("owner%d@example.com", round))
				store.SeedInvitationEmailsForTest(t, st, owner.Account.ID, store.MaxInvitationEmailsPerAccountPerDay-1, now)
				invitations := make([]store.Invitation, racers)
				for i := range invitations {
					invitations[i] = inviteAt(t, st, owner.Account.ID, owner.PersonalTeam, fmt.Sprintf("racer-%d-%d@example.com", round, i), now)
				}
				type result struct {
					ok  bool
					err error
				}
				results := make(chan result, racers)
				start := make(chan struct{})
				for _, inv := range invitations {
					go func() {
						<-start
						withheld, err := st.AsOperator().ClaimInvitationEmail(context.Background(), inv.ID)
						results <- result{err == nil && withheld == "", err}
					}()
				}
				close(start)
				sent := 0
				for range racers {
					r := <-results
					var limit *store.InvitationEmailLimitError
					switch {
					case r.err == nil && r.ok:
						sent++
					case errors.As(r.err, &limit):
					default:
						t.Fatalf("round %d: concurrent claim = %v, %v", round, r.ok, r.err)
					}
				}
				if sent != 1 {
					t.Fatalf("round %d: %d concurrent claims took the account's last invitation email, want 1", round, sent)
				}
			}
		})
	}
}

func TestInvitationEmailGoesOnlyToOpenInvitationsFromALiveAccount(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	team := owner.PersonalTeam

	withdrawn := inviteAt(t, st, owner.Account.ID, team, "withdrawn@example.com", now)
	if err := tenant(t, st, team).WithdrawInvitationForTest(t, ctx, withdrawn.ID, now); err != nil {
		t.Fatal(err)
	}
	guest := signIn(t, st, "g", "accepted@example.com")
	accepted := inviteAt(t, st, owner.Account.ID, team, "accepted@example.com", now)
	if err := acceptInvitation(t, st, guest.Account.ID, accepted); err != nil {
		t.Fatal(err)
	}
	orphaned := inviteAt(t, st, owner.Account.ID, team, "orphaned@example.com", now)
	// safety: account deletion blanks invited_by; such a send must not count under one digest every deleted
	// inviter would share.
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `UPDATE invitations SET invited_by = '' WHERE id = ?`), orphaned.ID); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]struct {
		inv    store.Invitation
		reason store.InvitationEmailWithheld
	}{
		"withdrawn": {withdrawn, store.InvitationEmailClosed},
		"accepted":  {accepted, store.InvitationEmailClosed},
		"orphaned":  {orphaned, store.InvitationEmailNoInviter},
	} {
		if got, err := claimReasonAt(t, st, want.inv, now); err != nil || got != want.reason {
			t.Errorf("%s invitation email = %q, %v; want withheld as %q", name, got, err, want.reason)
		}
	}
	if n := countWhere(t, st, "limit_events", "kind IN ('invitation_email_account', 'invitation_email_recipient')"); n != 0 {
		t.Fatalf("withheld invitation emails left %d log rows, want 0", n)
	}
}

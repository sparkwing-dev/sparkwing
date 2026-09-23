package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func signUpAt(t *testing.T, st *store.Store, p store.SignInProfile, c store.SignUpConditions, now time.Time) store.SignInResult {
	t.Helper()
	res, err := st.ResolveSignIn(context.Background(), p, c, now)
	if err != nil {
		t.Fatalf("ResolveSignIn(%s): %v", p.Email, err)
	}
	return res
}

func requireWaitlisted(t *testing.T, res store.SignInResult, reason string) {
	t.Helper()
	if !res.NewAccount || res.WaitlistReason != reason || !res.Account.Waitlisted ||
		res.PersonalTeam != "" || res.Account.ActiveTeam != "" {
		t.Fatalf("sign-up = %+v, want a new waitlisted account without a team, reason %q", res, reason)
	}
}

func requireAdmitted(t *testing.T, res store.SignInResult) {
	t.Helper()
	if res.WaitlistReason != "" || res.Account.Waitlisted || res.Account.ActiveTeam == "" {
		t.Fatalf("sign-up = %+v, want an admitted account with a team", res)
	}
}

func setLimits(t *testing.T, st *store.Store, l store.SignUpLimits) {
	t.Helper()
	if _, err := st.SetSignUpLimits(context.Background(), l); err != nil {
		t.Fatal(err)
	}
}

func TestSignUpGateDefaultsToOpenWithTheDefaultLimits(t *testing.T) {
	st := storetest.Open(t)
	g, err := st.SignUpGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g.Mode != store.SignUpOpen || g.Limits != store.DefaultSignUpLimits() {
		t.Fatalf("gate = %+v", g)
	}
	requireAdmitted(t, signIn(t, st, "s1", "pat@example.com"))
}

// Waitlisting is for new accounts. An account that exists when the gate
// closes keeps signing in to its team, and one that lost every team still
// gets its personal space back.
func TestOperatorWaitlistGatesOnlyNewAccounts(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	old := signIn(t, st, "s-old", "old@example.com")
	orphan := signIn(t, st, "s-orphan", "orphan@example.com")
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`DELETE FROM memberships WHERE account_id = ?`), orphan.Account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSignUpMode(ctx, store.SignUpWaitlist, "launch", "root", time.Now()); err != nil {
		t.Fatal(err)
	}

	again := signIn(t, st, "s-old", "old@example.com")
	if again.NewAccount || again.Account.Waitlisted || again.Account.ActiveTeam != old.Account.ActiveTeam {
		t.Fatalf("existing account after the gate closed = %+v", again)
	}
	back := signIn(t, st, "s-orphan", "orphan@example.com")
	if back.Account.Waitlisted || back.PersonalTeam == "" {
		t.Fatalf("existing account with no team = %+v, want its personal space", back)
	}
	linked := signUpAt(t, st, store.SignInProfile{
		Provider: store.ProviderGitHub, Subject: "99", Email: "old@example.com", EmailVerified: true,
	}, store.SignUpConditions{}, time.Now())
	if !linked.Linked || linked.Account.Waitlisted {
		t.Fatalf("a new identity linking to an existing account = %+v", linked)
	}

	requireWaitlisted(t, signIn(t, st, "s-new", "new@example.com"), store.WaitlistReasonOperator)
	returning := signIn(t, st, "s-new", "new@example.com")
	if returning.NewAccount || returning.PersonalTeam != "" || !returning.Account.Waitlisted {
		t.Fatalf("a waitlisted account signing in again = %+v", returning)
	}
	if _, err := st.CreateTeam(ctx, returning.Account.ID, "new-team", "", time.Now()); !errors.Is(err, store.ErrWaitlisted) {
		t.Fatalf("waitlisted CreateTeam = %v, want ErrWaitlisted", err)
	}

	if _, err := st.SetSignUpMode(ctx, store.SignUpOpen, "", "root", time.Now()); err != nil {
		t.Fatal(err)
	}
	requireAdmitted(t, signIn(t, st, "s-next", "next@example.com"))
}

// A team inviting a waitlisted person vouches for them: the invitation still
// works, and it grants that one team and nothing more.
func TestAWaitlistedAccountMayAcceptAnInvitation(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	owner := signIn(t, st, "s-owner", "owner@example.com")
	if _, err := st.SetSignUpMode(ctx, store.SignUpWaitlist, "", "root", time.Now()); err != nil {
		t.Fatal(err)
	}
	waiting := signIn(t, st, "s-w", "w@example.com")
	requireWaitlisted(t, waiting, store.WaitlistReasonOperator)

	inv, err := tenant(t, st, owner.Account.ActiveTeam).CreateInvitation(ctx, owner.Account.ID, "w@example.com", store.RoleEditor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	team, err := st.AcceptInvitation(ctx, waiting.Account.ID, inv.ID, time.Now())
	if err != nil || team != owner.Account.ActiveTeam {
		t.Fatalf("AcceptInvitation = %q, %v", team, err)
	}
	acct, err := st.Account(ctx, waiting.Account.ID)
	if err != nil || acct.ActiveTeam != owner.Account.ActiveTeam || !acct.Waitlisted {
		t.Fatalf("account after accepting = %+v, %v", acct, err)
	}
	if _, err := st.CreateTeam(ctx, acct.ID, "own-team", "", time.Now()); !errors.Is(err, store.ErrWaitlisted) {
		t.Fatalf("CreateTeam after accepting = %v, want ErrWaitlisted", err)
	}

	approved, err := st.ApproveWaitlisted(ctx, []string{acct.ID}, time.Now())
	if err != nil || len(approved) != 1 || approved[0].ActiveTeam != owner.Account.ActiveTeam {
		t.Fatalf("approve = %+v, %v", approved, err)
	}
	members, err := st.AccountMemberships(ctx, acct.ID)
	if err != nil || len(members) != 1 {
		t.Fatalf("an approved account that already had a team got another: %+v, %v", members, err)
	}
}

func TestTheGateClosesItselfPastTheHourlyLimit(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	setLimits(t, st, store.SignUpLimits{HourlyLimit: 3})
	now := time.Now()
	for i := range 3 {
		requireAdmitted(t, signUpAt(t, st, googleProfile(fmt.Sprintf("h%d", i), fmt.Sprintf("h%d@example.com", i)),
			store.SignUpConditions{}, now))
	}
	fourth := signUpAt(t, st, googleProfile("h3", "h3@example.com"), store.SignUpConditions{}, now)
	requireWaitlisted(t, fourth, store.WaitlistReasonHourly)
	if fourth.GateClosed == nil || fourth.GateClosed.Source != store.WaitlistReasonHourly {
		t.Fatalf("the sign-up that crossed the limit reported no closure: %+v", fourth.GateClosed)
	}
	g, err := st.SignUpGate(ctx)
	if err != nil || g.Mode != store.SignUpWaitlist || g.Source != store.WaitlistReasonHourly || g.SetBy != "automatic" {
		t.Fatalf("gate after the burst = %+v, %v", g, err)
	}
	// safety: the closure latches, so a burst that pauses does not reopen the gate by itself.
	later := signUpAt(t, st, googleProfile("h4", "h4@example.com"), store.SignUpConditions{}, now.Add(2*time.Hour))
	requireWaitlisted(t, later, store.WaitlistReasonHourly)
	if later.GateClosed != nil {
		t.Fatal("an already closed gate reported closing again")
	}

	reopened := now.Add(2 * time.Hour)
	if _, err := st.SetSignUpMode(ctx, store.SignUpOpen, "reviewed", "root", reopened); err != nil {
		t.Fatal(err)
	}
	requireAdmitted(t, signUpAt(t, st, googleProfile("h5", "h5@example.com"), store.SignUpConditions{}, reopened))
}

// Negative control: waitlisted accounts hold no space, so a burst of them
// leaves the limit for the people it admits.
func TestWaitlistedSignUpsDoNotCountTowardTheLimit(t *testing.T) {
	st := storetest.Open(t)
	setLimits(t, st, store.SignUpLimits{HourlyLimit: 2, GitHubMinAccountDays: 7})
	now := time.Now()
	for i := range 4 {
		requireWaitlisted(t, signUpAt(t, st, githubProfile(fmt.Sprintf("y%d", i), fmt.Sprintf("y%d@example.com", i), now),
			store.SignUpConditions{}, now), store.WaitlistReasonGitHubAge)
	}
	requireAdmitted(t, signUpAt(t, st, googleProfile("a0", "a0@example.com"), store.SignUpConditions{}, now))
}

// Negative control: a zero limit is off, so the same burst admits everyone.
func TestAZeroLimitNeverClosesTheGate(t *testing.T) {
	st := storetest.Open(t)
	setLimits(t, st, store.SignUpLimits{})
	now := time.Now()
	for i := range 6 {
		requireAdmitted(t, signUpAt(t, st, googleProfile(fmt.Sprintf("z%d", i), fmt.Sprintf("z%d@example.com", i)),
			store.SignUpConditions{}, now))
	}
}

func TestTheGateClosesItselfPastTheDailyLimit(t *testing.T) {
	st := storetest.Open(t)
	setLimits(t, st, store.SignUpLimits{HourlyLimit: 100, DailyLimit: 2})
	start := time.Now().Add(-10 * time.Hour)
	for i := range 2 {
		requireAdmitted(t, signUpAt(t, st, googleProfile(fmt.Sprintf("d%d", i), fmt.Sprintf("d%d@example.com", i)),
			store.SignUpConditions{}, start.Add(time.Duration(i)*3*time.Hour)))
	}
	third := signUpAt(t, st, googleProfile("d2", "d2@example.com"), store.SignUpConditions{}, start.Add(9*time.Hour))
	requireWaitlisted(t, third, store.WaitlistReasonDaily)
}

// Negative control for the daily window: the same accounts spread over more
// than a day never cross it.
func TestSignUpsOutsideTheDayDoNotCount(t *testing.T) {
	st := storetest.Open(t)
	setLimits(t, st, store.SignUpLimits{DailyLimit: 2})
	start := time.Now().Add(-72 * time.Hour)
	for i := range 3 {
		requireAdmitted(t, signUpAt(t, st, googleProfile(fmt.Sprintf("e%d", i), fmt.Sprintf("e%d@example.com", i)),
			store.SignUpConditions{}, start.Add(time.Duration(i)*25*time.Hour)))
	}
}

// A closed free tier waitlists new accounts while it lasts and records
// nothing, so the gate reopens as soon as storage does.
func TestAClosedFreeTierWaitlistsWithoutLatching(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	requireWaitlisted(t, signUpAt(t, st, googleProfile("f1", "f1@example.com"),
		store.SignUpConditions{FreeTierClosed: true}, time.Now()), store.WaitlistReasonFreeTier)
	g, err := st.SignUpGate(ctx)
	if err != nil || g.Mode != store.SignUpOpen {
		t.Fatalf("gate = %+v, %v; a free-tier closure must not be stored", g, err)
	}
	requireAdmitted(t, signUpAt(t, st, googleProfile("f2", "f2@example.com"), store.SignUpConditions{}, time.Now()))
}

func TestTheDeploymentSettingWaitlistsEveryNewAccount(t *testing.T) {
	st := storetest.Open(t)
	old := signIn(t, st, "o1", "o1@example.com")
	force := store.SignUpConditions{ForceWaitlist: true}
	requireWaitlisted(t, signUpAt(t, st, googleProfile("n1", "n1@example.com"), force, time.Now()),
		store.WaitlistReasonDeployment)
	again := signUpAt(t, st, googleProfile("o1", "o1@example.com"), force, time.Now())
	if again.Account.Waitlisted || again.Account.ActiveTeam != old.Account.ActiveTeam {
		t.Fatalf("existing account under the deployment waitlist = %+v", again)
	}
}

func githubProfile(sub, email string, created time.Time) store.SignInProfile {
	return store.SignInProfile{
		Provider: store.ProviderGitHub, Subject: sub, Email: email, EmailVerified: true,
		GivenName: "Octo", ProviderAccountCreatedAt: created,
	}
}

func TestYoungGitHubAccountsAreWaitlisted(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	none := store.SignUpConditions{}
	requireWaitlisted(t, signUpAt(t, st, githubProfile("1", "young@example.com", now.Add(-2*24*time.Hour)), none, now),
		store.WaitlistReasonGitHubAge)
	requireWaitlisted(t, signUpAt(t, st, githubProfile("2", "unknown@example.com", time.Time{}), none, now),
		store.WaitlistReasonGitHubAge)
	requireAdmitted(t, signUpAt(t, st, githubProfile("3", "old@example.com", now.Add(-8*24*time.Hour)), none, now))
	// safety: Google states no account age, so the age check must leave a Google sign-up alone.
	requireAdmitted(t, signUpAt(t, st, googleProfile("g1", "g1@example.com"), none, now))

	setLimits(t, st, store.SignUpLimits{GitHubMinAccountDays: 0})
	requireAdmitted(t, signUpAt(t, st, githubProfile("4", "young2@example.com", now.Add(-time.Hour)), none, now))
}

func TestApprovingTheOldestAdmitsInArrivalOrder(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	force := store.SignUpConditions{ForceWaitlist: true}
	start := time.Now().Add(-30 * time.Minute)
	var ids []string
	for i := range 3 {
		res := signUpAt(t, st, googleProfile(fmt.Sprintf("w%d", i), fmt.Sprintf("w%d@example.com", i)),
			force, start.Add(time.Duration(i)*time.Minute))
		ids = append(ids, res.Account.ID)
	}
	waiting, err := st.WaitlistedAccounts(ctx, 0)
	if err != nil || len(waiting) != 3 || waiting[0].ID != ids[0] || waiting[2].ID != ids[2] ||
		waiting[0].Reason != store.WaitlistReasonDeployment {
		t.Fatalf("waitlist = %+v, %v", waiting, err)
	}
	counts, err := st.SignUpCounts(ctx, time.Now())
	if err != nil || counts.Waitlisted != 3 || counts.LastHour != 3 || counts.LastDay != 3 {
		t.Fatalf("counts = %+v, %v", counts, err)
	}

	approved, err := st.ApproveOldestWaitlisted(ctx, 2, time.Now())
	if err != nil || len(approved) != 2 || approved[0].ID != ids[0] || approved[1].ID != ids[1] {
		t.Fatalf("approve oldest = %+v, %v", approved, err)
	}
	for _, a := range approved {
		if a.Waitlisted || a.ActiveTeam == "" {
			t.Fatalf("approved account = %+v, want a personal space", a)
		}
		if _, err := st.CreateTeam(ctx, a.ID, store.Team("t-"+a.ID[:8]), "", time.Now()); err != nil {
			t.Fatalf("an approved account cannot create a team: %v", err)
		}
	}
	again, err := st.ApproveWaitlisted(ctx, []string{ids[0], ids[2]}, time.Now())
	if err != nil || len(again) != 1 || again[0].ID != ids[2] {
		t.Fatalf("approving an admitted account again = %+v, %v", again, err)
	}
	if left, _ := st.WaitlistedAccounts(ctx, 0); len(left) != 0 {
		t.Fatalf("waitlist after approving everyone = %+v", left)
	}
	returning := signUpAt(t, st, googleProfile("w0", "w0@example.com"), force, time.Now())
	if returning.NewAccount || returning.Account.Waitlisted || returning.PersonalTeam != "" {
		t.Fatalf("approved account signing in = %+v", returning)
	}
}

func TestSignUpInputsAreValidated(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	if _, err := store.ParseSignUpMode("closed"); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("ParseSignUpMode(closed) = %v", err)
	}
	if _, err := st.SetSignUpLimits(ctx, store.SignUpLimits{HourlyLimit: -1}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("negative limit = %v", err)
	}
	if _, err := st.ApproveOldestWaitlisted(ctx, 0, time.Now()); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("approve zero = %v", err)
	}
}

package controller_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth/githubtest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type signUpStatus struct {
	State   string   `json:"state"`
	Reasons []string `json:"reasons"`
	Mode    string   `json:"mode"`
	Source  string   `json:"source"`
	SetBy   string   `json:"set_by"`
	Limits  struct {
		HourlyLimit          int `json:"hourly_limit"`
		DailyLimit           int `json:"daily_limit"`
		HourlyWarn           int `json:"hourly_warn"`
		GitHubMinAccountDays int `json:"github_min_account_days"`
	} `json:"limits"`
	Counts struct {
		LastHour   int `json:"last_hour"`
		LastDay    int `json:"last_day"`
		Waitlisted int `json:"waitlisted"`
	} `json:"counts"`
	FreeTier        string `json:"free_tier"`
	VelocityWarning bool   `json:"velocity_warning"`
}

func (f *identityFixture) signUpStatus() signUpStatus {
	f.t.Helper()
	var st signUpStatus
	if code := f.call("GET", "/api/v1/signups", "Bearer "+f.admin, nil, &st); code != http.StatusOK {
		f.t.Fatalf("GET /api/v1/signups = %d", code)
	}
	return st
}

func (f *identityFixture) setSignUp(body map[string]any) signUpStatus {
	f.t.Helper()
	var st signUpStatus
	if code := f.call("PUT", "/api/v1/signups", "Bearer "+f.admin, body, &st); code != http.StatusOK {
		f.t.Fatalf("PUT /api/v1/signups %v = %d", body, code)
	}
	return st
}

func (f *identityFixture) me(auth string) meBody {
	f.t.Helper()
	var me meBody
	if code := f.call("GET", "/api/v1/me", auth, nil, &me); code != http.StatusOK {
		f.t.Fatalf("GET /api/v1/me = %d", code)
	}
	return me
}

func TestSignUpGateRoutesAreTheOperators(t *testing.T) {
	f := newIdentityFixture(t)
	st := f.signUpStatus()
	if st.State != "open" || st.Mode != "open" || st.Limits.HourlyLimit != store.DefaultSignUpHourlyLimit ||
		st.Limits.DailyLimit != store.DefaultSignUpDailyLimit || st.Limits.HourlyWarn != store.DefaultSignUpHourlyWarn ||
		st.Limits.GitHubMinAccountDays != store.DefaultGitHubMinAccountDays || st.FreeTier != "open" {
		t.Fatalf("default status = %+v", st)
	}
	owner := f.signIn(person("g-o", "owner@example.com", "Owner"))
	auth := sessionAuth(owner.SessionID)
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/v1/signups"},
		{"PUT", "/api/v1/signups"},
		{"GET", "/api/v1/signups/waitlist"},
		{"POST", "/api/v1/signups/waitlist/approve"},
	} {
		if code := f.call(r.method, r.path, auth, map[string]any{"mode": "open"}, nil); code != http.StatusForbidden {
			t.Errorf("%s %s as a team owner = %d, want 403", r.method, r.path, code)
		}
	}
	for _, body := range []map[string]any{{}, {"mode": "closed"}, {"hourly_limit": -1}} {
		if code := f.call("PUT", "/api/v1/signups", "Bearer "+f.admin, body, nil); code != http.StatusBadRequest {
			t.Errorf("PUT %v = %d, want 400", body, code)
		}
	}
	for _, body := range []map[string]any{{}, {"oldest": 1, "account_ids": []string{"x"}}} {
		if code := f.call("POST", "/api/v1/signups/waitlist/approve", "Bearer "+f.admin, body, nil); code != http.StatusBadRequest {
			t.Errorf("approve %v = %d, want 400", body, code)
		}
	}
}

// The whole waitlist path through the API: a new person lands on the list
// with no team, an existing person is untouched, an invitation still lets the
// newcomer into the inviting team, and an operator's approval gives them
// their own space on the same session.
func TestWaitlistedSignUpThroughTheAPI(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.signIn(person("g-o", "owner@example.com", "Owner"))
	st := f.setSignUp(map[string]any{"mode": "waitlist", "reason": "launch"})
	if st.State != "waitlist" || st.Source != store.WaitlistReasonOperator || !strings.Contains(st.SetBy, "root") {
		t.Fatalf("status after closing = %+v", st)
	}

	again := f.signIn(person("g-o", "owner@example.com", "Owner"))
	if again.Waitlisted || again.ActiveTeam == nil || again.ActiveTeam.Slug != owner.ActiveTeam.Slug {
		t.Fatalf("existing account while waitlisted = %+v", again)
	}

	newcomer := f.signIn(person("g-n", "new@example.com", "New"))
	if !newcomer.Waitlisted || newcomer.ActiveTeam != nil {
		t.Fatalf("new sign-up = %+v, want waitlisted with no team", newcomer)
	}
	auth := sessionAuth(newcomer.SessionID)
	if me := f.me(auth); !me.Waitlisted || me.ActiveTeam != nil || len(me.Memberships) != 0 {
		t.Fatalf("/me = %+v", me)
	}
	if code := f.call("POST", "/api/v1/teams", auth, map[string]string{"slug": "bot-team"}, nil); code != http.StatusForbidden {
		t.Fatalf("waitlisted create team = %d, want 403", code)
	}
	if code := f.call("GET", "/api/v1/runs", auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("waitlisted list runs = %d, want 403", code)
	}

	var list struct {
		Accounts []struct {
			ID     string `json:"id"`
			Email  string `json:"email"`
			Reason string `json:"reason"`
		} `json:"accounts"`
	}
	f.call("GET", "/api/v1/signups/waitlist", "Bearer "+f.admin, nil, &list)
	if len(list.Accounts) != 1 || list.Accounts[0].ID != newcomer.User.ID || list.Accounts[0].Reason != "operator" {
		t.Fatalf("waitlist = %+v", list)
	}

	ownerAuth := sessionAuth(owner.SessionID)
	joiner := signedIn{auth: auth, id: newcomer.User.ID}
	f.join(signedIn{auth: ownerAuth, team: owner.ActiveTeam.Slug}, joiner, "new@example.com", "reader")
	if me := f.me(auth); me.ActiveTeam == nil || me.ActiveTeam.Slug != owner.ActiveTeam.Slug || !me.Waitlisted {
		t.Fatalf("/me after accepting an invitation = %+v", me)
	}
	if w := f.whoami(auth); w.Team != owner.ActiveTeam.Slug || w.Role != "reader" {
		t.Fatalf("whoami after accepting = %+v", w)
	}

	later := f.signIn(person("g-l", "later@example.com", "Later"))
	var approved struct {
		Approved []struct {
			ID         string `json:"id"`
			ActiveTeam string `json:"active_team"`
		} `json:"approved"`
	}
	if code := f.call("POST", "/api/v1/signups/waitlist/approve", "Bearer "+f.admin,
		map[string]any{"account_ids": []string{later.User.ID}}, &approved); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	if len(approved.Approved) != 1 || approved.Approved[0].ActiveTeam != "later" {
		t.Fatalf("approved = %+v", approved)
	}
	me := f.me(sessionAuth(later.SessionID))
	if me.Waitlisted || me.ActiveTeam == nil || me.ActiveTeam.Slug != "later" || me.ActiveTeam.Role != "owner" {
		t.Fatalf("/me on the waitlisted session after approval = %+v", me)
	}
	if w := f.whoami(sessionAuth(later.SessionID)); w.Team != "later" || w.Role != "owner" {
		t.Fatalf("whoami after approval = %+v", w)
	}
}

func TestApprovingTheOldestRunsTheNotifier(t *testing.T) {
	var mu sync.Mutex
	var notified []string
	raw, pub := multiTeamLicense(t)
	f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub, configure: func(s *controller.Server) {
		s.WithSignUpWaitlist(true).WithSignUpApprovalNotifier(func(_ context.Context, a store.Account) error {
			mu.Lock()
			defer mu.Unlock()
			notified = append(notified, a.Email)
			return nil
		})
	}})
	for i := range 3 {
		out := f.signIn(person(fmt.Sprintf("g-%d", i), fmt.Sprintf("p%d@example.com", i), "P"))
		if !out.Waitlisted {
			t.Fatalf("sign-up %d under the deployment waitlist was admitted", i)
		}
	}
	if st := f.signUpStatus(); st.State != "waitlist" || len(st.Reasons) != 1 || st.Reasons[0] != "deployment" ||
		st.Counts.Waitlisted != 3 {
		t.Fatalf("status = %+v", st)
	}
	if code := f.call("POST", "/api/v1/signups/waitlist/approve", "Bearer "+f.admin,
		map[string]any{"oldest": 2}, nil); code != http.StatusOK {
		t.Fatalf("approve oldest = %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notified) != 2 || notified[0] != "p0@example.com" || notified[1] != "p1@example.com" {
		t.Fatalf("notified = %v", notified)
	}
}

func TestAClosedFreeTierWaitlistsNewAccounts(t *testing.T) {
	var mu sync.Mutex
	state := controller.FreeTierClosed
	raw, pub := multiTeamLicense(t)
	f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub, configure: func(s *controller.Server) {
		s.WithFreeTier(func(context.Context) (controller.FreeTierState, error) {
			mu.Lock()
			defer mu.Unlock()
			return state, nil
		})
	}})
	if out := f.signIn(person("g-1", "one@example.com", "One")); !out.Waitlisted {
		t.Fatal("a new account was admitted while the free tier was closed")
	}
	if st := f.signUpStatus(); st.State != "waitlist" || st.FreeTier != "closed" || st.Mode != "open" {
		t.Fatalf("status = %+v", st)
	}
	mu.Lock()
	state = controller.FreeTierThrottled
	mu.Unlock()
	if out := f.signIn(person("g-2", "two@example.com", "Two")); out.Waitlisted || out.ActiveTeam == nil {
		t.Fatalf("a throttled free tier waitlisted a new account: %+v", out)
	}
}

func metricValue(t *testing.T, base, series string) float64 {
	t.Helper()
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), series+" "); ok {
			v, err := strconv.ParseFloat(rest, 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	t.Fatalf("/metrics has no series %s", series)
	return 0
}

// The sign-up that crosses the hourly limit closes the gate and says so in a
// metric, and the lower warn threshold fires first without closing anything.
func TestSignUpVelocityWarnsThenClosesTheGate(t *testing.T) {
	f := newIdentityFixture(t)
	f.setSignUp(map[string]any{"hourly_warn": 1, "hourly_limit": 3})
	const (
		closed     = `sparkwing_signup_gate_closed_total{reason="hourly_signups"}`
		warnings   = `sparkwing_signup_velocity_warnings_total`
		waitlisted = `sparkwing_signups_total{outcome="waitlisted",reason="hourly_signups"}`
	)
	closedBefore, warnBefore, waitBefore := metricValue(t, f.url, closed), metricValue(t, f.url, warnings),
		metricValue(t, f.url, waitlisted)

	f.signIn(person("v-0", "v0@example.com", "V"))
	if st := f.signUpStatus(); st.VelocityWarning || metricValue(t, f.url, warnings) != warnBefore {
		t.Fatalf("one sign-up already warned: %+v", st)
	}
	f.signIn(person("v-1", "v1@example.com", "V"))
	f.signIn(person("v-2", "v2@example.com", "V"))
	if st := f.signUpStatus(); !st.VelocityWarning || st.State != "open" || st.Counts.LastHour != 3 {
		t.Fatalf("status past the warn threshold = %+v", st)
	}
	if got := metricValue(t, f.url, warnings); got != warnBefore+1 {
		t.Fatalf("velocity warnings = %v, want one per crossing (%v before)", got, warnBefore)
	}
	if got := metricValue(t, f.url, closed); got != closedBefore {
		t.Fatalf("the gate closed before the hourly limit: %v", got)
	}

	fourth := f.signIn(person("v-3", "v3@example.com", "V"))
	if !fourth.Waitlisted {
		t.Fatal("the sign-up past the hourly limit was admitted")
	}
	st := f.signUpStatus()
	if st.State != "waitlist" || st.Mode != "waitlist" || st.Source != store.WaitlistReasonHourly || st.SetBy != "automatic" {
		t.Fatalf("status after the burst = %+v", st)
	}
	if metricValue(t, f.url, closed) != closedBefore+1 || metricValue(t, f.url, waitlisted) != waitBefore+1 {
		t.Fatal("the closure or the waitlisted sign-up was not counted")
	}

	f.setSignUp(map[string]any{"mode": "open", "reason": "reviewed the burst"})
	if out := f.signIn(person("v-4", "v4@example.com", "V")); out.Waitlisted {
		t.Fatal("reopening the gate did not restart the hourly window")
	}
}

func TestYoungGitHubAccountsAreWaitlistedAtSignIn(t *testing.T) {
	f := newIdentityFixture(t)
	young := ghPerson(901, "fresh", "fresh@example.com")
	young.CreatedAt = time.Now().Add(-2 * 24 * time.Hour)
	if out := f.signInGitHub(young); !out.Waitlisted || out.ActiveTeam != nil {
		t.Fatalf("a two-day-old GitHub account = %+v, want waitlisted", out)
	}
	var list struct {
		Accounts []struct {
			Reason string `json:"reason"`
		} `json:"accounts"`
	}
	f.call("GET", "/api/v1/signups/waitlist", "Bearer "+f.admin, nil, &list)
	if len(list.Accounts) != 1 || list.Accounts[0].Reason != store.WaitlistReasonGitHubAge {
		t.Fatalf("waitlist = %+v", list)
	}
	// safety: the fake's default account is a year old, the negative control for the age check.
	if out := f.signInGitHub(ghPerson(902, "settled", "settled@example.com")); out.Waitlisted {
		t.Fatal("an established GitHub account was waitlisted")
	}
	f.setSignUp(map[string]any{"github_min_account_days": 1})
	younger := githubtest.Person{
		ID: 903, Login: "newer", Emails: []githubtest.Email{{Email: "newer@example.com", Primary: true, Verified: true}},
		CreatedAt: time.Now().Add(-2 * 24 * time.Hour),
	}
	if out := f.signInGitHub(younger); out.Waitlisted {
		t.Fatal("a lowered minimum age still waitlisted a two-day-old account")
	}
}

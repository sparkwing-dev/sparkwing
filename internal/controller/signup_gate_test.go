package controller_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/githubauth/githubtest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
	VelocityWarning bool `json:"velocity_warning"`
}

func (f *identityFixture) signUpStatus() signUpStatus {
	f.t.Helper()
	var st signUpStatus
	if code := f.call("GET", "/api/v1/operator/signups", "Bearer "+f.admin, nil, &st); code != http.StatusOK {
		f.t.Fatalf("GET /api/v1/operator/signups = %d", code)
	}
	return st
}

func (f *identityFixture) setSignUp(body map[string]any) signUpStatus {
	f.t.Helper()
	var st signUpStatus
	if code := f.call("PUT", "/api/v1/operator/signups", "Bearer "+f.admin, body, &st); code != http.StatusOK {
		f.t.Fatalf("PUT /api/v1/operator/signups %v = %d", body, code)
	}
	return st
}

// safety: a waitlisted account reaches no team, so the operator is admitted
// before the controller lists it.
func (f *identityFixture) operatorAuth() string {
	f.t.Helper()
	op := person("g-operator", "korey@example.com", "Korey")
	out := f.signIn(op)
	if out.Waitlisted {
		if _, err := f.store.ApproveWaitlisted(context.Background(), []string{out.User.ID}, "test"); err != nil {
			f.t.Fatal(err)
		}
		out = f.signIn(op)
	}
	f.srv.WithOperatorAccounts([]string{out.User.ID})
	return sessionAuth(out.SessionID)
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
		st.Limits.GitHubMinAccountDays != store.DefaultGitHubMinAccountDays {
		t.Fatalf("default status = %+v", st)
	}
	owner := f.signIn(person("g-o", "owner@example.com", "Owner"))
	auth := sessionAuth(owner.SessionID)
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/v1/operator/signups"},
		{"PUT", "/api/v1/operator/signups"},
	} {
		if code := f.call(r.method, r.path, auth, map[string]any{"mode": "open"}, nil); code != http.StatusForbidden {
			t.Errorf("%s %s as a team owner = %d, want 403", r.method, r.path, code)
		}
	}
	for _, body := range []map[string]any{{}, {"mode": "closed"}, {"hourly_limit": -1}} {
		if code := f.call("PUT", "/api/v1/operator/signups", "Bearer "+f.admin, body, nil); code != http.StatusBadRequest {
			t.Errorf("PUT %v = %d, want 400", body, code)
		}
	}
	operator := f.operatorAuth()
	for _, body := range []map[string]any{{}, {"oldest": 1, "account_ids": []string{"x"}}} {
		if code := f.call("POST", "/api/v1/operator/waitlist/approve", operator, body, nil); code != http.StatusBadRequest {
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
	operator := f.operatorAuth()
	st := f.setSignUp(map[string]any{"mode": "waitlist", "reason": "launch"})
	if st.State != "waitlist" || st.Source != store.WaitlistReasonOperator || st.SetBy != "bootstrap:admin" {
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
	f.call("GET", "/api/v1/operator/waitlist", operator, nil, &list)
	if len(list.Accounts) != 1 || list.Accounts[0].ID != newcomer.User.ID || list.Accounts[0].Reason != "operator" {
		t.Fatalf("waitlist = %+v", list)
	}

	ownerAuth := sessionAuth(owner.SessionID)
	joiner := signedIn{auth: auth, id: newcomer.User.ID}
	f.join(signedIn{auth: ownerAuth, team: owner.ActiveTeam.Slug}, joiner, "new@example.com", "reader")
	if me := f.me(auth); me.ActiveTeam == nil || me.ActiveTeam.Slug != owner.ActiveTeam.Slug || me.ActiveTeam.Role != "reader" || !me.Waitlisted {
		t.Fatalf("/me after accepting an invitation = %+v", me)
	}
	if w := f.whoami(auth); w.Team != owner.ActiveTeam.Slug || w.Principal != newcomer.User.ID || len(w.Scopes) != 2 || !slices.Contains(w.Scopes, "runs.read") || !slices.Contains(w.Scopes, "logs.read") {
		t.Fatalf("whoami after accepting = %+v", w)
	}

	later := f.signIn(person("g-l", "later@example.com", "Later"))
	var approved struct {
		Approved []struct {
			ID         string `json:"id"`
			ActiveTeam string `json:"active_team"`
		} `json:"approved"`
	}
	if code := f.call("POST", "/api/v1/operator/waitlist/approve", operator,
		map[string]any{"account_ids": []string{later.User.ID}}, &approved); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	if len(approved.Approved) != 1 || approved.Approved[0].ActiveTeam != "later" {
		t.Fatalf("approved = %+v", approved)
	}
	laterAuth := sessionAuth(later.SessionID)
	me := f.me(laterAuth)
	if me.Waitlisted || me.ActiveTeam != nil || len(me.Memberships) != 1 || me.Memberships[0].Slug != "later" || me.Memberships[0].Role != "owner" {
		t.Fatalf("/me on the waitlisted session after approval = %+v", me)
	}
	if code := f.call("POST", "/api/v1/me/active-team", laterAuth, map[string]string{"slug": "later"}, nil); code != http.StatusNoContent {
		t.Fatalf("select approved team on the existing session = %d, want 204", code)
	}
	if me := f.me(laterAuth); me.ActiveTeam == nil || me.ActiveTeam.Slug != "later" || me.ActiveTeam.Role != "owner" {
		t.Fatalf("/me after selecting the approved team = %+v", me)
	}
	if w := f.whoami(laterAuth); w.Team != "later" || w.Principal != later.User.ID || !slices.Contains(w.Scopes, "team.admin") {
		t.Fatalf("whoami after approval and selection = %+v", w)
	}
}

func TestApprovingTheOldestRunsTheNotifier(t *testing.T) {
	var mu sync.Mutex
	var notified []string
	f := newIdentityFixtureWith(t, fixtureOpts{configure: func(s *controller.Server) {
		s.WithSignUpWaitlist(true).WithSignUpApprovalNotifier(func(_ context.Context, a store.Account) error {
			mu.Lock()
			defer mu.Unlock()
			notified = append(notified, a.Email)
			return nil
		})
	}})
	operator := f.operatorAuth()
	for i := range 3 {
		f.advance(time.Second)
		out := f.signIn(person(fmt.Sprintf("g-%d", i), fmt.Sprintf("p%d@example.com", i), "P"))
		if !out.Waitlisted {
			t.Fatalf("sign-up %d under the deployment waitlist was admitted", i)
		}
	}
	if st := f.signUpStatus(); st.State != "waitlist" || len(st.Reasons) != 1 || st.Reasons[0] != "deployment" ||
		st.Counts.Waitlisted != 3 {
		t.Fatalf("status = %+v", st)
	}
	if code := f.call("POST", "/api/v1/operator/waitlist/approve", operator,
		map[string]any{"oldest": 2}, nil); code != http.StatusOK {
		t.Fatalf("approve oldest = %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notified) != 2 || notified[0] != "p0@example.com" || notified[1] != "p1@example.com" {
		t.Fatalf("notified = %v", notified)
	}
}

func (f *identityFixture) metricValue(series string) float64 {
	t := f.t
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.url+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		t.Fatalf("GET /metrics = %d: %s", resp.StatusCode, body)
	}
	sc := bufio.NewScanner(resp.Body)
	// safety: the registry is process-wide, so a long series another test left behind must not end the scan early.
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), series+" "); ok {
			v, err := strconv.ParseFloat(rest, 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan /metrics: %v", err)
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
	closedBefore, warnBefore, waitBefore := f.metricValue(closed), f.metricValue(warnings),
		f.metricValue(waitlisted)

	f.signIn(person("v-0", "v0@example.com", "V"))
	if st := f.signUpStatus(); st.VelocityWarning || f.metricValue(warnings) != warnBefore {
		t.Fatalf("one sign-up already warned: %+v", st)
	}
	f.signIn(person("v-1", "v1@example.com", "V"))
	f.signIn(person("v-2", "v2@example.com", "V"))
	if st := f.signUpStatus(); !st.VelocityWarning || st.State != "open" || st.Counts.LastHour != 3 {
		t.Fatalf("status past the warn threshold = %+v", st)
	}
	if got := f.metricValue(warnings); got != warnBefore+1 {
		t.Fatalf("velocity warnings = %v, want one per crossing (%v before)", got, warnBefore)
	}
	if got := f.metricValue(closed); got != closedBefore {
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
	if f.metricValue(closed) != closedBefore+1 || f.metricValue(waitlisted) != waitBefore+1 {
		t.Fatal("the closure or the waitlisted sign-up was not counted")
	}

	f.advance(time.Second)
	f.setSignUp(map[string]any{"mode": "open", "reason": "reviewed the burst"})
	if out := f.signIn(person("v-4", "v4@example.com", "V")); out.Waitlisted {
		t.Fatal("reopening the gate did not restart the hourly window")
	}
}

func TestYoungGitHubAccountsAreWaitlistedAtSignIn(t *testing.T) {
	f := newIdentityFixture(t)
	young := ghPerson(901, "fresh", "fresh@example.com")
	young.CreatedAt = teststore.Now(f.store).Add(-2 * 24 * time.Hour)
	if out := f.signInGitHub(young); !out.Waitlisted || out.ActiveTeam != nil {
		t.Fatalf("a two-day-old GitHub account = %+v, want waitlisted", out)
	}
	var list struct {
		Accounts []struct {
			Reason string `json:"reason"`
		} `json:"accounts"`
	}
	f.call("GET", "/api/v1/operator/waitlist", f.operatorAuth(), nil, &list)
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
		CreatedAt: teststore.Now(f.store).Add(-2 * 24 * time.Hour),
	}
	if out := f.signInGitHub(younger); out.Waitlisted {
		t.Fatal("a lowered minimum age still waitlisted a two-day-old account")
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestAnOperatorAccountIsAdmittedAtSignIn(t *testing.T) {
	f := newIdentityFixture(t)
	f.setSignUp(map[string]any{"mode": "waitlist", "reason": "launch"})
	waiting := f.signIn(person("g-op", "op@example.com", "Op"))
	if !waiting.Waitlisted {
		t.Fatalf("sign-up while the gate is closed = %+v, want waitlisted", waiting)
	}
	f.srv.WithOperatorAccounts([]string{waiting.User.ID})
	again := f.signIn(person("g-op", "op@example.com", "Op"))
	if again.Waitlisted || again.ActiveTeam == nil {
		t.Fatalf("operator sign-in = %+v, want admitted with a team", again)
	}
	if me := f.me(sessionAuth(again.SessionID)); me.Waitlisted || me.ActiveTeam == nil {
		t.Fatalf("/me = %+v", me)
	}
	if other := f.signIn(person("g-x", "x@example.com", "X")); !other.Waitlisted {
		t.Fatal("a non-operator sign-up skipped the waitlist")
	}
}

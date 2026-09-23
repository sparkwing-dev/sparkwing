package controller_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type capabilities struct {
	Teams struct {
		Enabled bool `json:"enabled"`
	} `json:"teams"`
	Auth struct {
		Providers []string `json:"providers"`
	} `json:"auth"`
}

type meBody struct {
	User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
	ActiveTeam *struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	} `json:"active_team"`
	Memberships []struct {
		Slug string `json:"slug"`
		Role string `json:"role"`
	} `json:"memberships"`
	Invitations []struct {
		ID       string `json:"id"`
		TeamSlug string `json:"team_slug"`
		Role     string `json:"role"`
	} `json:"invitations"`
	Waitlisted bool `json:"waitlisted"`
}

func TestGoogleSignInCreatesAnAccountAndItsPersonalSpace(t *testing.T) {
	f := newIdentityFixture(t)
	out := f.signIn(person("g-alice", "Alice.Smith@Example.com", "Alice"))
	if out.User.Email != "alice.smith@example.com" || out.User.ID == "" {
		t.Fatalf("user = %+v", out.User)
	}
	if out.ActiveTeam == nil || out.ActiveTeam.Slug != "alice-smith" ||
		out.ActiveTeam.DisplayName != "Alice's space" || out.ActiveTeam.Role != "owner" {
		t.Fatalf("active_team = %+v", out.ActiveTeam)
	}

	var me meBody
	if code := f.call("GET", "/api/v1/me", sessionAuth(out.SessionID), nil, &me); code != http.StatusOK {
		t.Fatalf("GET /me = %d", code)
	}
	if me.ActiveTeam == nil || me.ActiveTeam.Slug != "alice-smith" || len(me.Memberships) != 1 {
		t.Fatalf("me = %+v", me)
	}
	w := f.whoami(sessionAuth(out.SessionID))
	if w.Team != "alice-smith" || w.Role != "owner" || !slices.Contains(w.Scopes, controller.ScopeTeamAdmin) {
		t.Fatalf("whoami = %+v", w)
	}
	if slices.Contains(w.Scopes, controller.ScopeAdmin) {
		t.Fatal("a team owner holds the deployment admin scope")
	}
}

func TestSecondSignInReturnsToTheSameAccountAndTeam(t *testing.T) {
	f := newIdentityFixture(t)
	first := f.signIn(person("g-bob", "bob@example.com", "Bob"))
	again := f.signIn(person("g-bob", "bob@example.com", "Bob"))
	if again.User.ID != first.User.ID || again.ActiveTeam.Slug != first.ActiveTeam.Slug {
		t.Fatalf("second sign-in: %+v then %+v", first, again)
	}
	if again.SessionID == first.SessionID {
		t.Fatal("a second sign-in reused the first session id")
	}
}

// A second Google subject on an address is a recycled address or another
// person, so it never joins the account that held the address first.
func TestASecondGoogleSubjectOnTheSameEmailGetsItsOwnAccount(t *testing.T) {
	f := newIdentityFixture(t)
	first := f.signIn(person("g-carol-1", "carol@example.com", "Carol"))
	second := f.signIn(person("g-carol-2", "carol@example.com", "Carol"))
	if second.User.ID == first.User.ID {
		t.Fatal("a second Google subject on the same address signed in to the first account")
	}
	if second.ActiveTeam == nil || second.ActiveTeam.Slug == first.ActiveTeam.Slug {
		t.Fatalf("second subject landed in %+v", second.ActiveTeam)
	}
}

// A user who lost their membership keeps a session for switching teams, and
// that session passes no route that admits any authenticated caller.
func TestARolelessSessionCannotReadServices(t *testing.T) {
	f := newIdentityFixture(t)
	s := f.signIn(person("g-l", "lena@example.com", "Lena"))
	if code := f.call("GET", "/api/v1/services", sessionAuth(s.SessionID), nil, nil); code == http.StatusForbidden || code == http.StatusUnauthorized {
		t.Fatalf("services with a role = %d", code)
	}
	if _, err := f.store.DB().Exec(`DELETE FROM memberships WHERE account_id = ?`, s.User.ID); err != nil {
		t.Fatal(err)
	}
	if code := f.call("GET", "/api/v1/services", sessionAuth(s.SessionID), nil, nil); code != http.StatusForbidden {
		t.Fatalf("services with no role = %d, want 403", code)
	}
	if code := f.call("GET", "/api/v1/me", sessionAuth(s.SessionID), nil, nil); code != http.StatusOK {
		t.Fatalf("/me with no role = %d, want 200", code)
	}
}

// Accounts exist only under a multi-team license, so their sessions stop
// authenticating when a controller over the same store runs without one.
func TestAccountSessionsStopWithoutALicense(t *testing.T) {
	f := newIdentityFixture(t)
	s := f.signIn(person("g-u", "una@example.com", "Una"))
	bare := controller.New(f.store, nil).EnableAuthFromStore()
	ts := httptest.NewServer(bare.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = bare.Shutdown(context.Background()) })
	unlicensed := &identityFixture{t: t, url: ts.URL, store: f.store}
	if code := unlicensed.call("GET", "/api/v1/auth/whoami", sessionAuth(s.SessionID), nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("account session on an unlicensed controller = %d, want 401", code)
	}
	if code := unlicensed.call("GET", "/api/v1/auth/session", sessionAuth(s.SessionID), nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("auth/session on an unlicensed controller = %d, want 401", code)
	}
}

func TestTeamCreationIsCappedPerUser(t *testing.T) {
	f := newIdentityFixture(t)
	s := sessionAuth(f.signIn(person("g-c", "cap@example.com", "Cap")).SessionID)
	for i := 2; i <= store.MaxCreatedTeams; i++ {
		if code := f.call("POST", "/api/v1/teams", s, map[string]string{"slug": fmt.Sprintf("cap-team-%d", i)}, nil); code != http.StatusCreated {
			t.Fatalf("team %d = %d", i, code)
		}
	}
	if code := f.call("POST", "/api/v1/teams", s, map[string]string{"slug": "cap-overflow"}, nil); code != http.StatusForbidden {
		t.Fatalf("team past the cap = %d, want 403", code)
	}
}

func TestPersonalSlugCollisionTakesTheSmallestFreeInteger(t *testing.T) {
	f := newIdentityFixture(t)
	a := f.signIn(person("g-1", "dana@one.example", "Dana"))
	b := f.signIn(person("g-2", "dana@two.example", "Dana"))
	c := f.signIn(person("g-3", "dana@three.example", "Dana"))
	if a.ActiveTeam.Slug != "dana" || b.ActiveTeam.Slug != "dana-2" || c.ActiveTeam.Slug != "dana-3" {
		t.Fatalf("slugs = %s, %s, %s", a.ActiveTeam.Slug, b.ActiveTeam.Slug, c.ActiveTeam.Slug)
	}
}

func TestSignInRefusesAnEmailGoogleHasNotVerified(t *testing.T) {
	f := newIdentityFixture(t)
	p := person("g-eve", "eve@example.com", "Eve")
	p.EmailVerified = false
	var start struct{ Verifier string }
	f.call("POST", "/api/v1/auth/oauth/google/start", "", map[string]string{"redirect_uri": dashRedirect}, &start)
	code := f.call("POST", "/api/v1/auth/oauth/google/exchange", "", map[string]string{
		"code": f.google.Code(p, start.Verifier, dashRedirect), "verifier": start.Verifier, "redirect_uri": dashRedirect,
	}, nil)
	if code != http.StatusForbidden {
		t.Fatalf("exchange with an unverified email = %d, want 403", code)
	}
}

// The code is bound to the verifier of the flow that started it, so a code
// minted for one flow and replayed with another flow's verifier is refused.
func TestExchangeRefusesAnotherFlowsVerifier(t *testing.T) {
	f := newIdentityFixture(t)
	code := f.google.Code(person("g-m", "mallory@example.com", "Mallory"), "attacker-verifier", dashRedirect)
	status := f.call("POST", "/api/v1/auth/oauth/google/exchange", "", map[string]string{
		"code": code, "verifier": "victim-verifier", "redirect_uri": dashRedirect,
	}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("exchange = %d, want 401", status)
	}
}

func TestOAuthRefusesARedirectOffTheAllowlist(t *testing.T) {
	f := newIdentityFixture(t)
	evil := "https://evil.example/auth/google/callback"
	if code := f.call("POST", "/api/v1/auth/oauth/google/start", "",
		map[string]string{"redirect_uri": evil}, nil); code != http.StatusBadRequest {
		t.Fatalf("start with an unlisted redirect = %d, want 400", code)
	}
	code := f.google.Code(person("g-x", "x@example.com", "X"), "v", evil)
	if status := f.call("POST", "/api/v1/auth/oauth/google/exchange", "", map[string]string{
		"code": code, "verifier": "v", "redirect_uri": evil,
	}, nil); status != http.StatusBadRequest {
		t.Fatalf("exchange with an unlisted redirect = %d, want 400", status)
	}
}

func TestCapabilitiesAnswerUnauthenticated(t *testing.T) {
	f := newIdentityFixture(t)
	var caps capabilities
	if code := f.call("GET", "/api/v1/capabilities", "", nil, &caps); code != http.StatusOK {
		t.Fatalf("capabilities = %d", code)
	}
	if !caps.Teams.Enabled || !slices.Equal(caps.Auth.Providers, []string{"google", "github"}) {
		t.Fatalf("capabilities = %+v", caps)
	}
}

func TestCreateTeamMakesTheCallerOwnerAndSwitchesToIt(t *testing.T) {
	f := newIdentityFixture(t)
	s := sessionAuth(f.signIn(person("g-f", "frank@example.com", "Frank")).SessionID)
	var team struct{ Slug, Role string }
	if code := f.call("POST", "/api/v1/teams", s,
		map[string]string{"slug": "Acme-Corp", "display_name": "Acme"}, &team); code != http.StatusCreated {
		t.Fatalf("POST /teams = %d", code)
	}
	if team.Slug != "acme-corp" || team.Role != "owner" {
		t.Fatalf("team = %+v", team)
	}
	if w := f.whoami(s); w.Team != "acme-corp" || w.Role != "owner" {
		t.Fatalf("whoami after create = %+v", w)
	}
	if code := f.call("POST", "/api/v1/teams", s, map[string]string{"slug": "acme-corp"}, nil); code != http.StatusConflict {
		t.Fatalf("creating a taken slug = %d, want 409", code)
	}
	for _, reserved := range []string{"default", "admin", "api"} {
		if code := f.call("POST", "/api/v1/teams", s, map[string]string{"slug": reserved}, nil); code != http.StatusBadRequest {
			t.Fatalf("creating reserved %q = %d, want 400", reserved, code)
		}
	}
}

func TestActiveTeamSwitchNeedsMembership(t *testing.T) {
	f := newIdentityFixture(t)
	g := sessionAuth(f.signIn(person("g-g", "gina@example.com", "Gina")).SessionID)
	f.signIn(person("g-h", "hank@example.com", "Hank"))
	if code := f.call("POST", "/api/v1/me/active-team", g, map[string]string{"slug": "hank"}, nil); code != http.StatusNotFound {
		t.Fatalf("switch into a team gina is not in = %d, want 404", code)
	}
	if code := f.call("POST", "/api/v1/me/active-team", g, map[string]string{"slug": "no-such-team"}, nil); code != http.StatusNotFound {
		t.Fatalf("switch into a missing team = %d, want 404", code)
	}
	f.call("POST", "/api/v1/teams", g, map[string]string{"slug": "gina-co"}, nil)
	if code := f.call("POST", "/api/v1/me/active-team", g, map[string]string{"slug": "gina"}, nil); code != http.StatusNoContent {
		t.Fatalf("switch back to own space = %d, want 204", code)
	}
	if w := f.whoami(g); w.Team != "gina" {
		t.Fatalf("whoami after switch = %+v", w)
	}
	if again := f.signIn(person("g-g", "gina@example.com", "Gina")); again.ActiveTeam.Slug != "gina" {
		t.Fatalf("second sign-in landed in %s", again.ActiveTeam.Slug)
	}
}

func TestIdentityRoutesRefuseABearerToken(t *testing.T) {
	f := newIdentityFixture(t)
	if code := f.call("GET", "/api/v1/me", "Bearer "+f.admin, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("GET /me with the operator token = %d, want 401", code)
	}
	if code := f.call("POST", "/api/v1/teams", "Bearer "+f.admin, map[string]string{"slug": "ops"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("POST /teams with the operator token = %d, want 401", code)
	}
}

func TestAnUnknownSessionIsRefused(t *testing.T) {
	f := newIdentityFixture(t)
	if code := f.call("GET", "/api/v1/runs", sessionAuth("not-a-session"), nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unknown session = %d, want 401", code)
	}
}

// A password session keeps working through the Session header, in the
// default team, with the scopes its user was created with.
func TestPasswordSessionAuthenticatesInTheDefaultTeam(t *testing.T) {
	f := newIdentityFixture(t)
	if _, err := f.store.CreateUser("ops", "correct-horse", []string{controller.ScopeRunsRead}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var login struct {
		SessionID string `json:"session_id"`
	}
	if code := f.call("POST", "/api/v1/auth/login", "",
		map[string]string{"username": "ops", "password": "correct-horse"}, &login); code != http.StatusOK {
		t.Fatalf("login = %d", code)
	}
	w := f.whoami(sessionAuth(login.SessionID))
	if w.Team != string(store.DefaultTeam) || !slices.Equal(w.Scopes, []string{controller.ScopeRunsRead}) {
		t.Fatalf("whoami = %+v", w)
	}
	if code := f.call("GET", "/api/v1/runs", sessionAuth(login.SessionID), nil, nil); code != http.StatusOK {
		t.Fatalf("GET /runs with a runs.read session = %d", code)
	}
	if code := f.call("GET", "/api/v1/tokens", sessionAuth(login.SessionID), nil, nil); code != http.StatusForbidden {
		t.Fatalf("GET /tokens with a runs.read session = %d, want 403", code)
	}
}

// Each unusable license leaves the controller holding one team: no team
// creation, no teams capability, no Google sign-in.
func TestUnusableLicensesLeaveTeamsDisabled(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	_, otherPriv := licensetest.NewKey(t)
	now := time.Now()
	valid := licensetest.Terms{
		Features: []string{license.FeatureMultiTeam}, IssuedTo: "t",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	}
	expired := valid
	expired.ExpiresAt = now.Add(-time.Minute)
	noFeature := valid
	noFeature.Features = []string{"billing"}
	good := licensetest.Sign(t, priv, valid)
	tampered := good[:len(good)-6] + "AAAAAA"
	cases := map[string]string{
		"absent":             "",
		"another key":        licensetest.Sign(t, otherPriv, valid),
		"tampered signature": tampered,
		"expired":            licensetest.Sign(t, priv, expired),
		"feature not listed": licensetest.Sign(t, priv, noFeature),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub})
			var caps capabilities
			f.call("GET", "/api/v1/capabilities", "", nil, &caps)
			if caps.Teams.Enabled || len(caps.Auth.Providers) != 0 {
				t.Fatalf("capabilities = %+v, want teams disabled and no providers", caps)
			}
			if code := f.call("POST", "/api/v1/auth/oauth/google/start", "",
				map[string]string{"redirect_uri": dashRedirect}, nil); code != http.StatusNotFound {
				t.Fatalf("start = %d, want 404", code)
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		f := newIdentityFixtureWith(t, fixtureOpts{license: good, key: pub})
		var caps capabilities
		f.call("GET", "/api/v1/capabilities", "", nil, &caps)
		if !caps.Teams.Enabled {
			t.Fatal("a valid license left teams disabled")
		}
	})
}

// A tampered payload keeps the original signature; it must not verify.
func TestTamperedLicensePayloadLeavesTeamsDisabled(t *testing.T) {
	pub, priv := licensetest.NewKey(t)
	now := time.Now()
	signed := licensetest.Sign(t, priv, licensetest.Terms{
		Features: []string{"billing"}, IssuedTo: "t", IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	lic, err := license.Verify(signed, pub, now)
	if err != nil || lic.Allows(license.FeatureMultiTeam, now) {
		t.Fatalf("setup: %v", err)
	}
	payload := []byte(`{"features":["multi-team"],"issued_to":"t","issued_at":"2026-01-01T00:00:00Z","expires_at":"2099-01-01T00:00:00Z"}`)
	_, sig := splitLicense(t, signed)
	f := newIdentityFixtureWith(t, fixtureOpts{license: licensetest.Encode(payload, sig), key: pub})
	var caps capabilities
	f.call("GET", "/api/v1/capabilities", "", nil, &caps)
	if caps.Teams.Enabled {
		t.Fatal("a license with a rewritten payload enabled teams")
	}
}

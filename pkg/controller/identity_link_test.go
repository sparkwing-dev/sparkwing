package controller_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth/githubtest"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type linkStartBody struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	Verifier     string `json:"verifier"`
}

type refusalBody struct {
	Code    string `json:"error"`
	Message string `json:"message"`
}

type identityBody struct {
	Provider  string `json:"provider"`
	Email     string `json:"email"`
	CreatedAt int64  `json:"created_at"`
}

type identitiesBody struct {
	Identities []identityBody `json:"identities"`
	Providers  []string       `json:"providers"`
}

func (f *identityFixture) linkStart(auth, provider string) linkStartBody {
	f.t.Helper()
	var start linkStartBody
	if code := f.call("POST", "/api/v1/me/identities/"+provider+"/link", auth,
		map[string]string{"redirect_uri": dashRedirect}, &start); code != http.StatusOK {
		f.t.Fatalf("link start %s = %d", provider, code)
	}
	return start
}

func (f *identityFixture) linkComplete(auth, provider string, start linkStartBody, code string, out any) int {
	f.t.Helper()
	return f.call("POST", "/api/v1/me/identities/"+provider+"/link/complete", auth, map[string]string{
		"state": start.State, "verifier": start.Verifier, "code": code, "redirect_uri": dashRedirect,
	}, out)
}

func (f *identityFixture) linkGitHub(auth string, p githubtest.Person, out any) int {
	f.t.Helper()
	start := f.linkStart(auth, "github")
	return f.linkComplete(auth, "github", start, f.github.Code(p, start.Verifier, dashRedirect), out)
}

func (f *identityFixture) identities(auth string) identitiesBody {
	f.t.Helper()
	var out identitiesBody
	if code := f.call("GET", "/api/v1/me/identities", auth, nil, &out); code != http.StatusOK {
		f.t.Fatalf("identities = %d", code)
	}
	return out
}

// A GitHub account whose address has nothing to do with the Google account
// links to it, and GitHub sign-in then lands in that account with its email
// unchanged.
func TestLinkGitHubWithAnotherEmailToAGoogleAccount(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	octo := ghPerson(501, "octo", "octo@elsewhere.org")

	var linked identityBody
	if code := f.linkGitHub(owner.auth, octo, &linked); code != http.StatusCreated {
		t.Fatalf("link = %d %+v", code, linked)
	}
	if linked.Provider != "github" || linked.Email != "octo@elsewhere.org" {
		t.Fatalf("linked = %+v", linked)
	}
	back := f.signInGitHub(octo)
	if back.User.ID != owner.id || back.User.Email != "owner@example.com" {
		t.Fatalf("GitHub sign-in after linking = %+v, want the Google account and its email", back.User)
	}
	ids := f.identities(owner.auth)
	if len(ids.Identities) != 2 || ids.Identities[0].Provider == ids.Identities[1].Provider ||
		!slices.Equal(ids.Providers, []string{"google", "github"}) {
		t.Fatalf("identities = %+v", ids)
	}
	// Control: a GitHub account nobody linked gets an account of its own.
	if stranger := f.signInGitHub(ghPerson(502, "stranger", "stranger@elsewhere.org")); stranger.User.ID == owner.id {
		t.Fatal("an unlinked GitHub sign-in reached the Google account")
	}
}

// A sign-in attached to another account is refused with guidance, and
// neither account changes.
func TestLinkRefusesASignInOnAnotherAccount(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	octo := ghPerson(601, "octo", "octo@example.com")
	other := f.signInGitHub(octo)

	var refused refusalBody
	if code := f.linkGitHub(owner.auth, octo, &refused); code != http.StatusConflict || refused.Code != "identity_linked_elsewhere" {
		t.Fatalf("link = %d %+v, want 409 identity_linked_elsewhere", code, refused)
	}
	if refused.Message == "" {
		t.Fatal("the refusal carries no message")
	}
	if ids := f.identities(owner.auth); len(ids.Identities) != 1 {
		t.Fatalf("owner identities after the refusal = %+v", ids)
	}
	if back := f.signInGitHub(octo); back.User.ID != other.User.ID {
		t.Fatal("the refused sign-in no longer reaches its own account")
	}
	if ids := f.identities(sessionAuth(other.SessionID)); len(ids.Identities) != 1 {
		t.Fatalf("other identities after the refusal = %+v", ids)
	}
	if code := f.linkGitHub(owner.auth, ghPerson(602, "free", "free@example.com"), nil); code != http.StatusCreated {
		t.Fatalf("control: linking a free GitHub account = %d", code)
	}
}

// A state finishes only the flow its account and session started, once.
func TestLinkRefusesAStateFromAnotherAccountSessionOrAReplay(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	stranger := f.user("g-stranger", "stranger@example.com")
	sameAccount := f.signIn(person("g-owner", "owner@example.com", "owner"))
	octo := ghPerson(701, "octo", "octo@example.com")

	start := f.linkStart(owner.auth, "github")
	code := func() string { return f.github.Code(octo, start.Verifier, dashRedirect) }
	for name, auth := range map[string]string{
		"another account": stranger.auth,
		"another session": sessionAuth(sameAccount.SessionID),
	} {
		var refused refusalBody
		if status := f.linkComplete(auth, "github", start, code(), &refused); status != http.StatusForbidden || refused.Code != "link_state_invalid" {
			t.Fatalf("%s finishing the flow = %d %+v, want 403 link_state_invalid", name, status, refused)
		}
	}
	var refused refusalBody
	if status := f.linkComplete(owner.auth, "google", start, code(), &refused); status != http.StatusForbidden || refused.Code != "link_state_invalid" {
		t.Fatalf("the flow finished for another provider = %d %+v, want 403 link_state_invalid", status, refused)
	}
	// Control: the account and session that started the flow finish it.
	if status := f.linkComplete(owner.auth, "github", start, code(), nil); status != http.StatusCreated {
		t.Fatalf("finishing the flow = %d", status)
	}
	if status := f.linkComplete(owner.auth, "github", start, code(), &refused); status != http.StatusForbidden || refused.Code != "link_state_invalid" {
		t.Fatalf("replaying the state = %d %+v, want 403 link_state_invalid", status, refused)
	}
	if ids := f.identities(stranger.auth); len(ids.Identities) != 1 {
		t.Fatalf("stranger identities = %+v", ids)
	}
}

func TestUnlinkRefusesTheLastSignInMethod(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	var refused refusalBody
	if code := f.call("DELETE", "/api/v1/me/identities/google", owner.auth, nil, &refused); code != http.StatusConflict || refused.Code != "last_sign_in_method" {
		t.Fatalf("unlink the only method = %d %+v, want 409 last_sign_in_method", code, refused)
	}
	if code := f.call("DELETE", "/api/v1/me/identities/github", owner.auth, nil, nil); code != http.StatusNotFound {
		t.Fatalf("unlink a provider the account lacks = %d, want 404", code)
	}
	if code := f.linkGitHub(owner.auth, ghPerson(801, "octo", "octo@example.com"), nil); code != http.StatusCreated {
		t.Fatalf("link = %d", code)
	}
	// Control: with GitHub beside it, Google unlinks.
	if code := f.call("DELETE", "/api/v1/me/identities/google", owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("unlink google beside github = %d, want 204", code)
	}
	if ids := f.identities(owner.auth); len(ids.Identities) != 1 || ids.Identities[0].Provider != "github" {
		t.Fatalf("identities after unlinking google = %+v", ids)
	}
}

// Unlinking GitHub leaves an installation the account connected bound to its
// team, and GitHub sign-in stops reaching the account.
func TestUnlinkThenSignInNoLongerReachesTheAccount(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	octo := ghPerson(901, "octo", "owner@example.com")
	if code := f.linkGitHub(owner.auth, octo, nil); code != http.StatusCreated {
		t.Fatalf("link = %d", code)
	}
	if back := f.signInGitHub(octo); back.User.ID != owner.id {
		t.Fatal("control: the linked GitHub sign-in did not reach the account")
	}
	ctx := context.Background()
	team, err := f.store.ForTeam(ctx, store.Team(owner.team))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.BindGitHubAppInstallation(ctx, store.GitHubAppInstallation{
		InstallationID: 77, AccountID: 901, AccountLogin: "octo", AccountType: "User",
		ConnectedBy: owner.id, GitHubUserID: 901,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	if code := f.call("DELETE", "/api/v1/me/identities/github", owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("unlink = %d, want 204", code)
	}
	after := f.signInGitHub(octo)
	if after.User.ID == owner.id {
		t.Fatal("GitHub sign-in reached the account after unlinking, by its matching email")
	}
	if f.whoami(owner.auth).Principal == "" {
		t.Fatal("the session that unlinked was ended")
	}
	installs, err := team.GitHubAppInstallations(ctx)
	if err != nil || len(installs) != 1 || installs[0].InstallationID != 77 {
		t.Fatalf("installations after unlinking GitHub = %+v, %v; want it still bound", installs, err)
	}
}

// Linking and unlinking change how an account is reached, so a session left
// open on a shared machine cannot do either.
func TestLinkAndUnlinkNeedARecentSignIn(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	if code := f.linkGitHub(owner.auth, ghPerson(1001, "octo", "octo@example.com"), nil); code != http.StatusCreated {
		t.Fatalf("control: link with a fresh sign-in = %d", code)
	}
	if _, err := f.store.DB().Exec(`UPDATE sessions SET created_at = created_at - 3600`); err != nil {
		t.Fatal(err)
	}
	var refused refusalBody
	if code := f.call("POST", "/api/v1/me/identities/github/link", owner.auth,
		map[string]string{"redirect_uri": dashRedirect}, &refused); code != http.StatusForbidden || refused.Code != "reauth_required" {
		t.Fatalf("link start with an hour-old sign-in = %d %+v, want 403 reauth_required", code, refused)
	}
	if code := f.call("DELETE", "/api/v1/me/identities/github", owner.auth, nil, &refused); code != http.StatusForbidden || refused.Code != "reauth_required" {
		t.Fatalf("unlink with an hour-old sign-in = %d %+v, want 403 reauth_required", code, refused)
	}
}

func TestLinkAttemptsAreRateLimitedPerAccount(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	other := f.user("g-other", "other@example.com")
	start := func(auth string, out any) int {
		return f.call("POST", "/api/v1/me/identities/github/link", auth, map[string]string{"redirect_uri": dashRedirect}, out)
	}
	for i := range 10 {
		if code := start(owner.auth, nil); code != http.StatusOK {
			t.Fatalf("attempt %d = %d", i+1, code)
		}
	}
	var refused refusalBody
	if code := start(owner.auth, &refused); code != http.StatusTooManyRequests || refused.Code != "rate_limited" {
		t.Fatalf("attempt 11 = %d %+v, want 429 rate_limited", code, refused)
	}
	if code := start(other.auth, nil); code != http.StatusOK {
		t.Fatalf("control: another account's attempt = %d", code)
	}
}

func TestLinkStartRefusesAProviderAlreadyLinked(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	var refused refusalBody
	if code := f.call("POST", "/api/v1/me/identities/google/link", owner.auth,
		map[string]string{"redirect_uri": dashRedirect}, &refused); code != http.StatusConflict || refused.Code != "provider_already_linked" {
		t.Fatalf("link google on a google account = %d %+v, want 409 provider_already_linked", code, refused)
	}
	start := f.linkStart(owner.auth, "github")
	u, err := url.Parse(start.AuthorizeURL)
	if err != nil || u.Query().Get("state") != start.State || u.Query().Get("redirect_uri") != dashRedirect {
		t.Fatalf("authorize_url %s does not carry the flow's state and redirect", start.AuthorizeURL)
	}
	if code := f.call("POST", "/api/v1/me/identities/github/link", owner.auth,
		map[string]string{"redirect_uri": "https://evil.example/cb"}, nil); code != http.StatusBadRequest {
		t.Fatalf("link start with an unlisted redirect = %d, want 400", code)
	}
	if code := f.call("POST", "/api/v1/me/identities/github/link", "Bearer "+f.admin,
		map[string]string{"redirect_uri": dashRedirect}, nil); code != http.StatusUnauthorized {
		t.Fatalf("link start with a bearer token = %d, want 401", code)
	}
}

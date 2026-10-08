package controller_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth/githubtest"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

type linkFlow struct {
	AuthorizeURL string
	State        string
	Verifier     string
	cookie       *http.Cookie
	outcome      string
}

const linked = "linked"

func signInsOutcome(t *testing.T, resp *browserResponse) string {
	t.Helper()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusSeeOther || err != nil {
		t.Fatalf("link answer = %d %q, want a redirect", resp.StatusCode, resp.Header.Get("Location"))
	}
	if loc.Path != "/account/sign-ins" {
		return "redirect:" + loc.Path
	}
	if loc.Query().Has("linked") {
		return linked
	}
	return loc.Query().Get("refused")
}

func (f *identityFixture) tryLinkStart(auth, provider string) linkFlow {
	f.t.Helper()
	resp := f.browserSend("POST", auth, "/auth/"+provider+"/link", url.Values{})
	if resp.StatusCode != http.StatusOK {
		return linkFlow{outcome: signInsOutcome(f.t, resp)}
	}
	cookie := responseCookie(resp, "__Host-sw_oauth")
	if cookie == nil {
		f.t.Fatal("link start set no flow cookie")
	}
	page := responseBody(f.t, resp)
	_, after, _ := strings.Cut(page, `content="0;url=`)
	authorize, _, _ := strings.Cut(after, `"`)
	u, err := url.Parse(authorize)
	if err != nil {
		f.t.Fatal(err)
	}
	return linkFlow{AuthorizeURL: authorize, State: u.Query().Get("state"), Verifier: flowVerifier(f.t, cookie), cookie: cookie}
}

func (f *identityFixture) linkStart(auth, provider string) linkFlow {
	f.t.Helper()
	start := f.tryLinkStart(auth, provider)
	if start.cookie == nil {
		f.t.Fatalf("link start %s refused: %s", provider, start.outcome)
	}
	return start
}

func (f *identityFixture) linkComplete(auth, provider string, start linkFlow, code string) string {
	f.t.Helper()
	flow := start.cookie
	callback := f.browserGet("/auth/"+provider+"/callback?"+url.Values{"code": {code}, "state": {start.State}}.Encode(), flow)
	if next := responseCookie(callback, "__Host-sw_oauth"); next != nil {
		flow = next
	}
	return signInsOutcome(f.t, f.browserSend("GET", auth, "/auth/"+provider+"/link/complete", nil, flow))
}

func (f *identityFixture) linkGitHub(auth string, p githubtest.Person) string {
	f.t.Helper()
	start := f.linkStart(auth, "github")
	return f.linkComplete(auth, "github", start, f.github.Code(p, start.Verifier, githubRedirect))
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

	if outcome := f.linkGitHub(owner.auth, octo); outcome != linked {
		t.Fatalf("link = %s", outcome)
	}
	ids := f.identities(owner.auth)
	if !slices.ContainsFunc(ids.Identities, func(id identityBody) bool {
		return id.Provider == "github" && id.Email == "octo@elsewhere.org"
	}) {
		t.Fatalf("identities after linking = %+v", ids)
	}
	back := f.signInGitHub(octo)
	if back.User.ID != owner.id || back.User.Email != "owner@example.com" {
		t.Fatalf("GitHub sign-in after linking = %+v, want the Google account and its email", back.User)
	}
	if len(ids.Identities) != 2 || ids.Identities[0].Provider == ids.Identities[1].Provider ||
		!slices.Equal(ids.Providers, []string{"google", "github"}) {
		t.Fatalf("identities = %+v", ids)
	}
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

	if outcome := f.linkGitHub(owner.auth, octo); outcome != "identity_linked_elsewhere" {
		t.Fatalf("link = %s, want identity_linked_elsewhere", outcome)
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
	if outcome := f.linkGitHub(owner.auth, ghPerson(602, "free", "free@example.com")); outcome != linked {
		t.Fatalf("control: linking a free GitHub account = %s", outcome)
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
	code := func() string { return f.github.Code(octo, start.Verifier, githubRedirect) }
	for name, auth := range map[string]string{
		"another account": stranger.auth,
		"another session": sessionAuth(sameAccount.SessionID),
	} {
		if outcome := f.linkComplete(auth, "github", start, code()); outcome != "link_state_invalid" {
			t.Fatalf("%s finishing the flow = %s, want link_state_invalid", name, outcome)
		}
	}
	if outcome := f.linkComplete(owner.auth, "google", start, code()); outcome != "link_state_invalid" {
		t.Fatalf("the flow finished for another provider = %s, want link_state_invalid", outcome)
	}
	if outcome := f.linkComplete(owner.auth, "github", start, code()); outcome != linked {
		t.Fatalf("finishing the flow = %s", outcome)
	}
	if outcome := f.linkComplete(owner.auth, "github", start, code()); outcome != "link_state_invalid" {
		t.Fatalf("replaying the state = %s, want link_state_invalid", outcome)
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
	if outcome := f.linkGitHub(owner.auth, ghPerson(801, "octo", "octo@example.com")); outcome != linked {
		t.Fatalf("link = %s", outcome)
	}
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
	if outcome := f.linkGitHub(owner.auth, octo); outcome != linked {
		t.Fatalf("link = %s", outcome)
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
	if outcome := f.linkGitHub(owner.auth, ghPerson(1001, "octo", "octo@example.com")); outcome != linked {
		t.Fatalf("control: link with a fresh sign-in = %s", outcome)
	}
	if _, err := f.store.DB().Exec(`UPDATE sessions SET created_at = created_at - 3600`); err != nil {
		t.Fatal(err)
	}
	if outcome := f.tryLinkStart(owner.auth, "github").outcome; outcome != "reauth_required" {
		t.Fatalf("link start with an hour-old sign-in = %s, want reauth_required", outcome)
	}
	var refused refusalBody
	if code := f.call("DELETE", "/api/v1/me/identities/github", owner.auth, nil, &refused); code != http.StatusForbidden || refused.Code != "reauth_required" {
		t.Fatalf("unlink with an hour-old sign-in = %d %+v, want 403 reauth_required", code, refused)
	}
}

func TestLinkCompletionNeedsARecentSignIn(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	start := f.linkStart(owner.auth, "github")
	if _, err := f.store.DB().Exec(`UPDATE sessions SET created_at = created_at - 3600`); err != nil {
		t.Fatal(err)
	}
	outcome := f.linkComplete(owner.auth, "github", start,
		f.github.Code(ghPerson(1201, "octo", "octo@example.com"), start.Verifier, githubRedirect))
	if outcome != "reauth_required" {
		t.Fatalf("link completion after sign-in expired = %s, want reauth_required", outcome)
	}
	if ids := f.identities(owner.auth); len(ids.Identities) != 1 {
		t.Fatalf("identities after refused completion = %+v", ids)
	}
}

func TestLinkAttemptsAreRateLimitedPerAccount(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	other := f.user("g-other", "other@example.com")
	for i := range 10 {
		if outcome := f.tryLinkStart(owner.auth, "github").outcome; outcome != "" {
			t.Fatalf("attempt %d = %s", i+1, outcome)
		}
	}
	if outcome := f.tryLinkStart(owner.auth, "github").outcome; outcome != "rate_limited" {
		t.Fatalf("attempt 11 = %s, want rate_limited", outcome)
	}
	if outcome := f.tryLinkStart(other.auth, "github").outcome; outcome != "" {
		t.Fatalf("control: another account's attempt = %s", outcome)
	}
}

func TestLinkStartRefusesAProviderAlreadyLinked(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("g-owner", "owner@example.com")
	if outcome := f.tryLinkStart(owner.auth, "google").outcome; outcome != "provider_already_linked" {
		t.Fatalf("link google on a google account = %s, want provider_already_linked", outcome)
	}
	start := f.linkStart(owner.auth, "github")
	u, err := url.Parse(start.AuthorizeURL)
	if err != nil || u.Query().Get("state") != start.State || u.Query().Get("redirect_uri") != githubRedirect {
		t.Fatalf("authorize_url %s does not carry the flow's state and redirect", start.AuthorizeURL)
	}

	csrf := f.csrfFor(owner.auth)
	form := url.Values{"csrf_token": {csrf}}
	req, err := http.NewRequest(http.MethodPost, f.url+"/auth/github/link", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example"
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: sessionIDOf(owner.auth)})
	req.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
	resp := f.browserDo(req)
	if outcome := signInsOutcome(t, resp); outcome != "link_failed" {
		t.Fatalf("link start from an unlisted host = %s, want link_failed", outcome)
	}

	bearer, err := http.NewRequest(http.MethodPost, f.url+"/auth/github/link", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	bearer.Host = dashHost
	bearer.Header.Set("Authorization", "Bearer "+f.admin)
	bearer.Header.Set("Origin", "http://"+dashHost)
	if resp := f.browserDo(bearer); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("link start with a bearer token and no browser session = %d, want 403", resp.StatusCode)
	}
}

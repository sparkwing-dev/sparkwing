package controller_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
	"github.com/sparkwing-dev/sparkwing/internal/githubauth/githubtest"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth/googletest"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const appCallback = "http://localhost:4343/github/app/callback"

const headSHA = "89abcdef0123456789abcdef0123456789abcdef"

type appFixture struct {
	*identityFixture
	app *githubapptest.GitHub
	srv *controller.Server
	// logs holds what every replica logs.
	logs *syncBuffer
	// replica starts another controller over the same store and App.
	replica func() (*controller.Server, string)
}

func newAppFixture(t *testing.T, opts ...func(*controller.Server) *controller.Server) *appFixture {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	google, gh, app := googletest.New(t), githubtest.New(t), githubapptest.New(t)
	app.AddInstallation(githubapptest.Installation{
		ID: 7, Account: githubapp.Account{ID: 70, Login: "acme", Type: "Organization"},
		Repos: []githubapptest.Repo{{ID: 701, FullName: "acme/widgets"}, {ID: 702, FullName: "acme/plans", Private: true}},
	})
	app.AddInstallation(githubapptest.Installation{
		ID: 8, Account: githubapp.Account{ID: 502, Login: "bob", Type: "User"},
		Repos: []githubapptest.Repo{{ID: 801, FullName: "bob/tools"}},
	})
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	replica := func() (*controller.Server, string) {
		srv := controller.New(st, logger).EnableAuthFromStore().
			WithLicense(license.Resolve(raw, pub, time.Now(), nil)).
			WithGoogleSignIn(googleauth.New(google.Config()), []string{dashRedirect}).
			WithGitHubSignIn(githubauth.New(gh.Config()), []string{dashRedirect, appCallback}).
			WithDashboardURL("https://dash.example.com").
			WithGitHubApp(app.Config())
		for _, opt := range opts {
			srv = opt(srv)
		}
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
		return srv, ts.URL
	}
	srv, url := replica()
	return &appFixture{
		identityFixture: &identityFixture{t: t, url: url, store: st, google: google, github: gh, admin: admin},
		app:             app, srv: srv, replica: replica, logs: logs,
	}
}

func (f *appFixture) ghUser(id int64, login string) signedIn {
	f.t.Helper()
	out := f.signInGitHub(ghPerson(id, login, login+"@example.com"))
	return signedIn{auth: sessionAuth(out.SessionID), id: out.User.ID, team: out.ActiveTeam.Slug}
}

type connectStart struct {
	InstallURL   string `json:"install_url"`
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	Verifier     string `json:"verifier"`
}

func (f *appFixture) start(who signedIn) connectStart {
	f.t.Helper()
	var s connectStart
	if code := f.call("POST", "/api/v1/team/github-app/connect", who.auth,
		map[string]string{"redirect_uri": appCallback}, &s); code != http.StatusOK {
		f.t.Fatalf("connect start = %d", code)
	}
	return s
}

var codeSeq int

func (f *appFixture) finish(who signedIn, s connectStart, verifier string, ghID, installation int64, orgs map[string]githubapp.OrgMembership) int {
	f.t.Helper()
	codeSeq++
	code := "code-" + string(rune('a'+codeSeq%26)) + time.Now().Format("150405.000000000")
	f.app.IssueCode(code, githubapp.User{ID: ghID, Login: "u"}, verifier, appCallback, orgs)
	return f.call("POST", "/api/v1/team/github-app/connect/complete", who.auth, map[string]any{
		"state": s.State, "verifier": verifier, "code": code, "installation_id": installation, "redirect_uri": appCallback,
	}, nil)
}

var acmeAdmin = map[string]githubapp.OrgMembership{"acme": {State: "active", Role: "admin"}}

func (f *appFixture) connect(who signedIn, ghID, installation int64, orgs map[string]githubapp.OrgMembership) {
	f.t.Helper()
	s := f.start(who)
	if code := f.finish(who, s, s.Verifier, ghID, installation, orgs); code != http.StatusCreated {
		f.t.Fatalf("connect installation %d = %d", installation, code)
	}
}

func (f *appFixture) installations(who signedIn) []int64 {
	f.t.Helper()
	var out struct {
		Installations []struct {
			InstallationID int64 `json:"installation_id"`
		} `json:"installations"`
	}
	if code := f.call("GET", "/api/v1/team/github-app", who.auth, nil, &out); code != http.StatusOK {
		f.t.Fatalf("show = %d", code)
	}
	var ids []int64
	for _, in := range out.Installations {
		ids = append(ids, in.InstallationID)
	}
	return ids
}

func TestGitHubAppConnectBindsAnInstallationItsOrgAdminProves(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	s := f.start(olga)
	if !strings.HasPrefix(s.InstallURL, f.app.URL+"/apps/sparkwing-test/installations/new?state=") ||
		!strings.Contains(s.AuthorizeURL, "code_challenge=") {
		t.Fatalf("start = %+v", s)
	}
	if code := f.finish(olga, s, s.Verifier, 501, 7, acmeAdmin); code != http.StatusCreated {
		t.Fatalf("complete = %d, want 201", code)
	}
	if ids := f.installations(olga); len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("installations = %v, want [7]", ids)
	}
	var caps struct {
		GitHubApp *struct {
			Slug string `json:"slug"`
		} `json:"github_app"`
	}
	f.call("GET", "/api/v1/capabilities", "", nil, &caps)
	if caps.GitHubApp == nil || caps.GitHubApp.Slug != "sparkwing-test" {
		t.Fatalf("capabilities github_app = %+v", caps.GitHubApp)
	}
}

func TestGitHubAppExistingInstallationPicker(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	bob := f.ghUser(502, "bob")
	f.connect(bob, 502, 8, nil)
	start := f.start(olga)
	f.app.IssueCode("existing-code", githubapp.User{ID: 501, Login: "olga"}, start.Verifier, appCallback, acmeAdmin)
	var available struct {
		Authorization string `json:"authorization"`
		Installations []struct {
			InstallationID     int64 `json:"installation_id"`
			ConnectedElsewhere bool  `json:"connected_elsewhere"`
		} `json:"installations"`
	}
	if code := f.call("POST", "/api/v1/team/github-app/connect/available", olga.auth, map[string]any{
		"state": start.State, "verifier": start.Verifier, "code": "existing-code", "redirect_uri": appCallback,
	}, &available); code != http.StatusOK {
		t.Fatalf("available = %d, want 200", code)
	}
	if len(available.Installations) != 1 || available.Installations[0].InstallationID != 7 || available.Authorization == "" {
		t.Fatalf("available = %+v, want administered installation 7 and a proof", available)
	}
	selectInstallation := func(who signedIn, state, proof string, id int64) int {
		return f.call("POST", "/api/v1/team/github-app/connect/select", who.auth, map[string]any{
			"state": state, "verifier": start.Verifier, "authorization": proof, "installation_id": id,
		}, nil)
	}
	if code := selectInstallation(olga, start.State, available.Authorization, 7); code != http.StatusCreated {
		t.Fatalf("administered installation = %d, want 201", code)
	}
	if ids := f.installations(olga); !slices.Equal(ids, []int64{7}) {
		t.Fatalf("bound installations = %v, want [7]", ids)
	}
	if code := selectInstallation(olga, start.State, available.Authorization, 7); code != http.StatusForbidden {
		t.Fatalf("replayed selection = %d, want 403", code)
	}
}

func TestGitHubAppExistingPickerOnlyBindsListedInstallations(t *testing.T) {
	selectInstallation := func(code string, id int64) (int, map[string]any) {
		t.Helper()
		f := newAppFixture(t)
		olga := f.ghUser(501, "olga")
		start := f.start(olga)
		f.app.IssueCode(code, githubapp.User{ID: 501, Login: "olga"}, start.Verifier, appCallback, acmeAdmin)
		var available struct {
			Authorization string `json:"authorization"`
			Installations []struct {
				InstallationID int64 `json:"installation_id"`
			} `json:"installations"`
		}
		if status := f.call("POST", "/api/v1/team/github-app/connect/available", olga.auth, map[string]any{
			"state": start.State, "verifier": start.Verifier, "code": code, "redirect_uri": appCallback,
		}, &available); status != http.StatusOK {
			t.Fatalf("available = %d", status)
		}
		if len(available.Installations) != 1 || available.Installations[0].InstallationID != 7 {
			t.Fatalf("available = %+v, want only installation 7", available)
		}
		f.app.AddInstallation(githubapptest.Installation{
			ID: 9, Account: githubapp.Account{ID: 70, Login: "acme", Type: "Organization"},
		})
		var response map[string]any
		status := f.call("POST", "/api/v1/team/github-app/connect/select", olga.auth, map[string]any{
			"state": start.State, "verifier": start.Verifier, "authorization": available.Authorization, "installation_id": id,
		}, &response)
		if id != 7 {
			if replay := f.call("POST", "/api/v1/team/github-app/connect/select", olga.auth, map[string]any{
				"state": start.State, "verifier": start.Verifier, "authorization": available.Authorization, "installation_id": 7,
			}, nil); replay != http.StatusForbidden {
				t.Fatalf("selection after failed attempt = %d, want 403", replay)
			}
		}
		return status, response
	}
	status, unlisted := selectInstallation("unlisted-existing", 9)
	if status != http.StatusNotFound {
		t.Fatalf("unlisted existing installation = %d, want 404", status)
	}
	status, missing := selectInstallation("missing-existing", 999)
	if status != http.StatusNotFound || !reflect.DeepEqual(unlisted, missing) {
		t.Fatalf("missing installation = %d %+v, want same 404 as unlisted %+v", status, missing, unlisted)
	}
	status, _ = selectInstallation("listed-existing", 7)
	if status != http.StatusCreated {
		t.Fatalf("listed installation = %d, want 201", status)
	}
}

func TestGitHubAppExistingPickerRefusesForeignIdentityAndBoundInstallation(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	bob := f.ghUser(502, "bob")
	f.connect(bob, 502, 7, acmeAdmin)
	start := f.start(olga)
	f.app.IssueCode("foreign-existing", githubapp.User{ID: 666, Login: "mallory"}, start.Verifier, appCallback, acmeAdmin)
	request := func(code string, out any) int {
		return f.call("POST", "/api/v1/team/github-app/connect/available", olga.auth, map[string]any{
			"state": start.State, "verifier": start.Verifier, "code": code, "redirect_uri": appCallback,
		}, out)
	}
	if code := request("foreign-existing", nil); code != http.StatusForbidden {
		t.Fatalf("foreign linked identity = %d, want 403", code)
	}
	f.app.IssueCode("admin-existing", githubapp.User{ID: 501, Login: "olga"}, start.Verifier, appCallback, acmeAdmin)
	var available struct {
		Authorization string `json:"authorization"`
		Installations []struct {
			InstallationID     int64 `json:"installation_id"`
			ConnectedElsewhere bool  `json:"connected_elsewhere"`
		} `json:"installations"`
	}
	if code := request("admin-existing", &available); code != http.StatusOK {
		t.Fatalf("available = %d", code)
	}
	if len(available.Installations) != 1 || !available.Installations[0].ConnectedElsewhere {
		t.Fatalf("bound installation = %+v, want connected_elsewhere", available.Installations)
	}
	var refused map[string]any
	if code := f.call("POST", "/api/v1/team/github-app/connect/select", olga.auth, map[string]any{
		"state": start.State, "verifier": start.Verifier, "authorization": available.Authorization, "installation_id": 7,
	}, &refused); code != http.StatusConflict {
		t.Fatalf("bound selection = %d, want 409", code)
	}
	if strings.Contains(strings.ToLower(refused["error"].(string)), "bob") {
		t.Fatalf("conflict names another team: %+v", refused)
	}
}

func TestGitHubAppExistingPickerHidesVisibleInstallationWithoutAdminMembership(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	start := f.start(olga)
	f.app.IssueCode("member-existing", githubapp.User{ID: 501, Login: "olga"}, start.Verifier, appCallback,
		map[string]githubapp.OrgMembership{"acme": {State: "active", Role: "member"}})
	var available struct {
		Installations []any `json:"installations"`
	}
	if code := f.call("POST", "/api/v1/team/github-app/connect/available", olga.auth, map[string]any{
		"state": start.State, "verifier": start.Verifier, "code": "member-existing", "redirect_uri": appCallback,
	}, &available); code != http.StatusOK {
		t.Fatalf("available = %d", code)
	}
	if len(available.Installations) != 0 {
		t.Fatalf("member sees %v, want no selectable installations", available.Installations)
	}
}

// The state and verifier are what bind a flow to the account, team and
// browser that started it. Each case below is otherwise a valid completion by
// an org admin, so the one check it names is all that stands in its way.
func TestGitHubAppConnectRefusesForgedReplayedAndForeignState(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	mallory := f.ghUser(666, "mallory")
	malloryAdmin := acmeAdmin

	cases := map[string]func() (signedIn, connectStart, string, int64){
		"state with a forged expiry": func() (signedIn, connectStart, string, int64) {
			s := f.start(olga)
			body, sig, _ := strings.Cut(s.State, ".")
			payload, _ := base64.RawURLEncoding.DecodeString(body)
			var st map[string]any
			_ = json.Unmarshal(payload, &st)
			st["e"] = time.Now().Add(24 * time.Hour).Unix()
			re, _ := json.Marshal(st)
			s.State = base64.RawURLEncoding.EncodeToString(re) + "." + sig
			return olga, s, s.Verifier, 501
		},
		"another browser's verifier": func() (signedIn, connectStart, string, int64) {
			s := f.start(olga)
			return olga, s, f.start(olga).Verifier, 501
		},
		"another account's state": func() (signedIn, connectStart, string, int64) {
			s := f.start(olga)
			return mallory, s, s.Verifier, 666
		},
		"no state": func() (signedIn, connectStart, string, int64) {
			s := f.start(olga)
			s.State = ""
			return olga, s, s.Verifier, 501
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			who, s, verifier, ghID := c()
			if code := f.finish(who, s, verifier, ghID, 7, malloryAdmin); code != http.StatusForbidden {
				t.Fatalf("complete = %d, want 403", code)
			}
		})
	}
	if ids := f.installations(olga); len(ids) != 0 {
		t.Fatalf("a refused flow bound %v", ids)
	}
	if ids := f.installations(mallory); len(ids) != 0 {
		t.Fatalf("a refused flow bound %v to mallory", ids)
	}

	s := f.start(olga)
	if code := f.finish(olga, s, s.Verifier, 501, 7, acmeAdmin); code != http.StatusCreated {
		t.Fatalf("control: an intact flow = %d, want 201", code)
	}
	if code := f.finish(olga, s, s.Verifier, 501, 7, acmeAdmin); code != http.StatusForbidden {
		t.Fatalf("replayed state = %d, want 403", code)
	}
}

// A state finishes one flow across every replica and restart, because the
// used nonce is kept in the store rather than in one controller's memory.
func TestGitHubAppConnectStateIsSingleUseAcrossReplicas(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	s := f.start(olga)
	_, other := f.replica()
	first := f.url
	f.url = other
	if code := f.finish(olga, s, s.Verifier, 501, 7, acmeAdmin); code != http.StatusCreated {
		t.Fatalf("control: completing on another replica = %d, want 201", code)
	}
	f.url = first
	if code := f.finish(olga, s, s.Verifier, 501, 7, acmeAdmin); code != http.StatusForbidden {
		t.Fatalf("the same state on the replica that issued it = %d, want 403", code)
	}
}

// Seeing an installation is not administering its account: a member, a
// stranger, and a GitHub user other than the account's linked one bind nothing.
func TestGitHubAppConnectNeedsTheAccountsAdmin(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	cases := map[string]struct {
		ghID         int64
		installation int64
		orgs         map[string]githubapp.OrgMembership
	}{
		"organization member who is not an admin": {501, 7, map[string]githubapp.OrgMembership{"acme": {State: "active", Role: "member"}}},
		"pending organization admin":              {501, 7, map[string]githubapp.OrgMembership{"acme": {State: "pending", Role: "admin"}}},
		"not in the organization at all":          {501, 7, nil},
		"another user's personal installation":    {501, 8, nil},
		"a GitHub user other than the linked one": {777, 7, acmeAdmin},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := f.start(olga)
			if code := f.finish(olga, s, s.Verifier, c.ghID, c.installation, c.orgs); code != http.StatusForbidden {
				t.Fatalf("complete = %d, want 403", code)
			}
		})
	}
	if ids := f.installations(olga); len(ids) != 0 {
		t.Fatalf("a refused flow bound %v", ids)
	}
	bob := f.ghUser(502, "bob")
	f.connect(bob, 502, 8, nil)

	googleOnly := f.user("g-1", "gina@example.com")
	if code := f.call("POST", "/api/v1/team/github-app/connect", googleOnly.auth,
		map[string]string{"redirect_uri": appCallback}, nil); code != http.StatusForbidden {
		t.Fatalf("connect with no linked GitHub identity = %d, want 403", code)
	}
}

func TestGitHubAppInstallationBelongsToOneTeam(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	s := f.start(bob)
	if code := f.finish(bob, s, s.Verifier, 502, 7, acmeAdmin); code != http.StatusConflict {
		t.Fatalf("second team's connect of a bound installation = %d, want 409", code)
	}
	if ids := f.installations(bob); len(ids) != 0 {
		t.Fatalf("bob's team holds %v", ids)
	}
	if code := f.call("DELETE", "/api/v1/github-app/installations/7", "Bearer "+f.admin, nil, nil); code != http.StatusNoContent {
		t.Fatalf("operator unbind = %d", code)
	}
	f.connect(bob, 502, 7, acmeAdmin)
	if ids := f.installations(bob); len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("after the operator's move bob's team holds %v", ids)
	}
}

func (f *appFixture) deliver(event string, payload any, signature string) (int, map[string]any) {
	f.t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	if signature == "" {
		signature = githubapptest.Sign(body)
	}
	req, err := http.NewRequest(http.MethodPost, f.url+"/webhooks/github-app", bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "delivery-"+time.Now().Format("150405.000000000"))
	req.Header.Set("X-Hub-Signature-256", signature)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func pushPayload(installation, repoID int64, repo, sha string) map[string]any {
	return map[string]any{
		"ref": "refs/heads/main", "before": strings.Repeat("0", 40), "after": sha,
		"installation": map[string]any{"id": installation},
		"repository":   map[string]any{"id": repoID, "full_name": repo, "pushed_at": time.Now().Unix()},
		"pusher":       map[string]any{"name": "olga"},
	}
}

func prPayload(installation, repoID int64, repo string, headRepoID int64) map[string]any {
	return map[string]any{
		"action": "opened", "number": 12,
		"installation": map[string]any{"id": installation},
		"repository":   map[string]any{"id": repoID, "full_name": repo},
		"pull_request": map[string]any{
			"updated_at": time.Now().UTC().Format(time.RFC3339),
			"head":       map[string]any{"ref": "feature", "sha": headSHA, "repo": map[string]any{"id": headRepoID, "full_name": "x/y"}},
			"base":       map[string]any{"ref": "main", "sha": strings.Repeat("1", 40), "repo": map[string]any{"id": repoID, "full_name": repo}},
			"user":       map[string]any{"login": "contributor"},
		},
	}
}

func (f *appFixture) subscribe(who signedIn, repo, pipeline string, body map[string]any) int {
	f.t.Helper()
	req := map[string]any{"repository": repo, "pipeline": pipeline, "push": true}
	for k, v := range body {
		req[k] = v
	}
	return f.call("PUT", "/api/v1/team/github-app/triggers", who.auth, req, nil)
}

func (f *appFixture) triggers(team string) []*store.Trigger {
	f.t.Helper()
	tn, err := f.store.ForTeam(context.Background(), store.Team(team))
	if err != nil {
		f.t.Fatal(err)
	}
	list, err := tn.ListTriggers(context.Background(), store.TriggerFilter{})
	if err != nil {
		f.t.Fatal(err)
	}
	return list
}

func TestGitHubAppWebhookSignature(t *testing.T) {
	f := newAppFixture(t)
	payload := map[string]any{"zen": "hi"}
	if code, _ := f.deliver("ping", payload, "sha256="+strings.Repeat("0", 64)); code != http.StatusUnauthorized {
		t.Fatalf("wrong signature = %d, want 401", code)
	}
	body, _ := json.Marshal(payload)
	if code, _ := f.deliver("ping", payload, githubapptest.SignWith("another-secret", body)); code != http.StatusUnauthorized {
		t.Fatalf("signature by another secret = %d, want 401", code)
	}
	if code, _ := f.deliver("ping", payload, "garbage"); code != http.StatusUnauthorized {
		t.Fatalf("malformed signature = %d, want 401", code)
	}
	if code, _ := f.deliver("ping", payload, ""); code != http.StatusOK {
		t.Fatalf("control: the App's signature = %d, want 200", code)
	}
}

// A delivery runs in the team its installation is bound to, for the pipelines
// that team subscribed, and a team can subscribe only a repository one of its
// own installations covers.
func TestGitHubAppPushRunsOnlyInTheInstallationsTeam(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	f.connect(bob, 502, 8, nil)

	if code := f.subscribe(olga, "acme/widgets", "build", nil); code != http.StatusOK {
		t.Fatalf("subscribe = %d", code)
	}
	if code := f.subscribe(bob, "acme/widgets", "steal", nil); code != http.StatusNotFound {
		t.Fatalf("bob subscribing a repository of olga's installation = %d, want 404", code)
	}
	if code := f.subscribe(bob, "bob/tools", "build", nil); code != http.StatusOK {
		t.Fatalf("bob subscribing his own repository = %d", code)
	}

	code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), "")
	if code != http.StatusAccepted || out["status"] != "dispatched" {
		t.Fatalf("push = %d %v", code, out)
	}
	got := f.triggers(olga.team)
	if len(got) != 1 || got[0].Pipeline != "build" || got[0].GitSHA != headSHA || got[0].GithubRepo != "widgets" {
		t.Fatalf("olga's triggers = %+v", got)
	}
	if got[0].TriggerEnv["GITHUB_EVENT_NAME"] != "push" {
		t.Fatalf("app push event = %q, want push", got[0].TriggerEnv["GITHUB_EVENT_NAME"])
	}
	if n := len(f.triggers(bob.team)); n != 0 {
		t.Fatalf("a push through olga's installation started %d runs in bob's team", n)
	}

	if code, out := f.deliver("push", pushPayload(7, 801, "bob/tools", headSHA), ""); code != http.StatusAccepted || out["status"] != "ignored" {
		t.Fatalf("push naming bob's repository through olga's installation = %d %v, want ignored", code, out)
	}
	if n := len(f.triggers(bob.team)); n != 0 {
		t.Fatalf("bob's team got %d runs from olga's installation", n)
	}

	if code, out := f.deliver("push", pushPayload(99, 701, "acme/widgets", headSHA), ""); code != http.StatusAccepted || out["status"] != "ignored" {
		t.Fatalf("push through an unbound installation = %d %v", code, out)
	}
}

func TestGitHubAppTagPushRequiresTagSubscription(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "build", nil); code != http.StatusOK {
		t.Fatal(code)
	}
	tag := pushPayload(7, 701, "acme/widgets", headSHA)
	tag["ref"] = "refs/tags/v1.2.3"
	if _, out := f.deliver("push", tag, ""); out["status"] != "ignored" {
		t.Fatalf("default subscription started tag: %v", out)
	}
	if code := f.subscribe(olga, "acme/widgets", "build", map[string]any{"push": false, "tags": []string{"v*"}}); code != http.StatusOK {
		t.Fatalf("tag-only subscription = %d", code)
	}
	var listed struct {
		Triggers []struct {
			Push bool     `json:"push"`
			Tags []string `json:"tags"`
		} `json:"triggers"`
	}
	if code := f.call("GET", "/api/v1/team/github-app/triggers", olga.auth, nil, &listed); code != http.StatusOK ||
		len(listed.Triggers) != 1 || listed.Triggers[0].Push || len(listed.Triggers[0].Tags) != 1 || listed.Triggers[0].Tags[0] != "v*" {
		t.Fatalf("listed tag subscription = %d %+v", code, listed)
	}
	branch := pushPayload(7, 701, "acme/widgets", headSHA)
	if _, out := f.deliver("push", branch, ""); out["status"] != "ignored" {
		t.Fatalf("tag-only subscription started branch: %v", out)
	}
	if _, out := f.deliver("push", tag, ""); out["status"] != "dispatched" {
		t.Fatalf("tag push = %v", out)
	}
	got := f.triggers(olga.team)
	if len(got) != 1 || got[0].GitBranch != "" || got[0].GitSHA != headSHA ||
		got[0].TriggerEnv["GITHUB_REF"] != "refs/tags/v1.2.3" ||
		got[0].TriggerEnv["GITHUB_REF_TYPE"] != "tag" || got[0].TriggerEnv["GITHUB_TAG"] != "v1.2.3" {
		t.Fatalf("tag trigger = %+v", got)
	}
	deleted := pushPayload(7, 701, "acme/widgets", headSHA)
	deleted["ref"], deleted["deleted"] = "refs/tags/v1.2.4", true
	if _, out := f.deliver("push", deleted, ""); out["status"] != "ignored" {
		t.Fatalf("deleted tag = %v", out)
	}
	if n := len(f.triggers(olga.team)); n != 1 {
		t.Fatalf("deleted tag left %d triggers", n)
	}
}

func TestGitHubAppTagPatternsFilterPushes(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "release", map[string]any{"push": false, "tags": []string{"v*"}}); code != http.StatusOK {
		t.Fatalf("pattern subscription = %d", code)
	}
	for _, tc := range []struct {
		ref, status string
	}{
		{"refs/tags/canary", "ignored"},
		{"refs/tags/v1/nested", "ignored"},
		{"refs/tags/v1.2.3", "dispatched"},
	} {
		payload := pushPayload(7, 701, "acme/widgets", headSHA)
		payload["ref"] = tc.ref
		if _, out := f.deliver("push", payload, ""); out["status"] != tc.status {
			t.Fatalf("%s = %v, want %s", tc.ref, out, tc.status)
		}
	}
	if got := len(f.triggers(olga.team)); got != 1 {
		t.Fatalf("tag patterns started %d runs, want 1", got)
	}
	if code := f.subscribe(olga, "acme/widgets", "release", map[string]any{"push": true, "tags": []string{}}); code != http.StatusOK {
		t.Fatalf("empty pattern list = %d", code)
	}
	payload := pushPayload(7, 701, "acme/widgets", headSHA)
	payload["ref"] = "refs/tags/v2.0.0"
	if _, out := f.deliver("push", payload, ""); out["status"] != "ignored" {
		t.Fatalf("empty pattern list started tag: %v", out)
	}
}

func TestGitHubAppTagPatternsValidateBounds(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	for _, patterns := range [][]string{
		{"["},
		{strings.Repeat("v", 129)},
		{""},
		{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"},
	} {
		if code := f.subscribe(olga, "acme/widgets", "release", map[string]any{"tags": patterns}); code != http.StatusBadRequest {
			t.Fatalf("invalid tag patterns %q accepted: %d", patterns, code)
		}
	}
}

func TestGitHubAppTagBooleanRejected(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "release", map[string]any{"push": false, "tags": true}); code != http.StatusBadRequest {
		t.Fatalf("boolean tags accepted: %d", code)
	}
}

func TestGitHubAppPushBranchSubscription(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "deploy", map[string]any{"branches": []string{"main", "release/*"}}); code != http.StatusOK {
		t.Fatalf("subscribe with branches = %d, want 200", code)
	}
	feature := pushPayload(7, 701, "acme/widgets", headSHA)
	feature["ref"] = "refs/heads/feature/risky"
	if code, out := f.deliver("push", feature, ""); code != http.StatusAccepted || out["status"] != "ignored" || out["reason"] == "" {
		t.Fatalf("nonmatching push = %d %v, want ignored with reason", code, out)
	}
	if n := len(f.triggers(olga.team)); n != 0 {
		t.Fatalf("nonmatching push started %d runs", n)
	}
	nested := pushPayload(7, 701, "acme/widgets", headSHA)
	nested["ref"] = "refs/heads/release/one/two"
	if code, out := f.deliver("push", nested, ""); code != http.StatusAccepted || out["status"] != "ignored" {
		t.Fatalf("nested release push = %d %v, want ignored by path.Match", code, out)
	}
	release := pushPayload(7, 701, "acme/widgets", headSHA)
	release["ref"] = "refs/heads/release/1.0"
	if code, out := f.deliver("push", release, ""); code != http.StatusAccepted || out["status"] != "dispatched" {
		t.Fatalf("matching push = %d %v, want dispatched", code, out)
	}
	if n := len(f.triggers(olga.team)); n != 1 {
		t.Fatalf("matching push started %d runs, want 1", n)
	}
	if code := f.subscribe(olga, "acme/widgets", "deploy", map[string]any{"branches": []string{}}); code != http.StatusOK {
		t.Fatalf("clear branch filter = %d", code)
	}
	feature["after"] = strings.Repeat("2", 40)
	if code, out := f.deliver("push", feature, ""); code != http.StatusAccepted || out["status"] != "dispatched" {
		t.Fatalf("push with empty filter = %d %v, want dispatched", code, out)
	}
}

func TestGitHubAppPullRequestBaseBranchSubscription(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "build", map[string]any{
		"push": false, "pull_request": true, "base_branches": []string{"main", "release/*"},
	}); code != http.StatusOK {
		t.Fatalf("subscribe with base_branches = %d, want 200", code)
	}
	feature := prPayload(7, 701, "acme/widgets", 701)
	feature["pull_request"].(map[string]any)["base"].(map[string]any)["ref"] = "feature"
	if code, out := f.deliver("pull_request", feature, ""); code != http.StatusAccepted || out["status"] != "ignored" || out["reason"] == "" {
		t.Fatalf("nonmatching PR base = %d %v, want ignored with reason", code, out)
	}
	if n := len(f.triggers(olga.team)); n != 0 {
		t.Fatalf("nonmatching PR base started %d runs", n)
	}
	if code, out := f.deliver("pull_request", prPayload(7, 701, "acme/widgets", 701), ""); code != http.StatusAccepted || out["status"] != "dispatched" {
		t.Fatalf("matching PR base = %d %v, want dispatched", code, out)
	}
	if n := len(f.triggers(olga.team)); n != 1 {
		t.Fatalf("matching PR base started %d runs, want 1", n)
	}
}

func TestGitHubAppSubscriptionRejectsInvalidBranchPatterns(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"too many push patterns", map[string]any{"branches": []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}}},
		{"long push pattern", map[string]any{"branches": []string{strings.Repeat("x", 129)}}},
		{"push pattern exceeds byte limit", map[string]any{"branches": []string{strings.Repeat("é", 65)}}},
		{"malformed push pattern", map[string]any{"branches": []string{"["}}},
		{"empty push pattern", map[string]any{"branches": []string{""}}},
		{"too many base patterns", map[string]any{"base_branches": []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}}},
		{"long base pattern", map[string]any{"base_branches": []string{strings.Repeat("x", 129)}}},
		{"malformed base pattern", map[string]any{"base_branches": []string{"["}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code := f.subscribe(olga, "acme/widgets", "build", tc.body); code != http.StatusBadRequest {
				t.Fatalf("invalid patterns = %d, want 400", code)
			}
		})
	}
	if code := f.subscribe(olga, "acme/widgets", "build", map[string]any{"branches": []string{strings.Repeat("x", 128)}}); code != http.StatusOK {
		t.Fatalf("128-byte pattern = %d, want 200", code)
	}
}

func TestGitHubAppBranchFilterLeavesTagPatternsAlone(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "deploy", map[string]any{
		"branches": []string{"main"}, "tags": []string{"v*"},
	}); code != http.StatusOK {
		t.Fatalf("subscribe with branches and tags = %d", code)
	}
	for _, tc := range []struct {
		ref, sha, status string
	}{
		{"refs/heads/feature", strings.Repeat("1", 40), "ignored"},
		{"refs/tags/canary", strings.Repeat("2", 40), "ignored"},
		{"refs/heads/main", strings.Repeat("3", 40), "dispatched"},
		{"refs/tags/v1.0.0", strings.Repeat("4", 40), "dispatched"},
	} {
		payload := pushPayload(7, 701, "acme/widgets", tc.sha)
		payload["ref"] = tc.ref
		if _, out := f.deliver("push", payload, ""); out["status"] != tc.status {
			t.Fatalf("%s = %v, want %s", tc.ref, out, tc.status)
		}
	}
	if n := len(f.triggers(olga.team)); n != 2 {
		t.Fatalf("branch and tag filters started %d runs, want 2", n)
	}
}

func TestGitHubAppRedeliveryStartsOneRun(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	payload := pushPayload(7, 701, "acme/widgets", headSHA)
	if code, _ := f.deliver("push", payload, ""); code != http.StatusAccepted {
		t.Fatal("first delivery refused")
	}
	_, out := f.deliver("push", payload, "")
	runs, _ := out["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["status"] != "duplicate" {
		t.Fatalf("redelivery = %v, want one duplicate", out)
	}
	if n := len(f.triggers(olga.team)); n != 1 {
		t.Fatalf("a redelivery left %d triggers, want 1", n)
	}
}

func TestGitHubAppUninstallUnbinds(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	if code, _ := f.deliver("installation", map[string]any{"action": "deleted", "installation": map[string]any{"id": 7}}, ""); code != http.StatusOK {
		t.Fatalf("uninstall = %d", code)
	}
	if ids := f.installations(olga); len(ids) != 0 {
		t.Fatalf("installations after uninstall = %v", ids)
	}
	if _, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), ""); out["status"] != "ignored" {
		t.Fatalf("push after uninstall = %v, want ignored", out)
	}
}

type claimedNode struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"node_id"`
}

func (f *appFixture) appWork(owner signedIn, runID, slug string) string {
	f.t.Helper()
	ctx := context.Background()
	tn, err := f.store.ForTeam(ctx, store.Team(owner.team))
	if err != nil {
		f.t.Fatal(err)
	}
	repo, _ := store.ParseGitHubRepo(slug)
	now := time.Now()
	if err := tn.CreateTriggerWithRun(ctx, store.Trigger{
		ID: runID, Pipeline: "build", RepoURL: "https://github.com/" + slug + ".git",
		GithubOwner: repo.Owner, GithubRepo: repo.Name, CreatedAt: now, GitBranch: "main", GitSHA: headSHA,
	}, store.Run{
		ID: runID, Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now,
		RepoURL: "https://github.com/" + slug + ".git", GithubOwner: repo.Owner, GithubRepo: repo.Name,
	}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: runID, NodeID: "compile", Status: "pending"}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.MarkNodeReady(ctx, runID, "compile"); err != nil {
		f.t.Fatal(err)
	}
	var m mintedRunner
	if code := f.call("POST", "/api/v1/team/runner-tokens", owner.auth,
		map[string]any{"name": "r-" + runID, "repos": []string{"github.com/*/*"}}, &m); code != http.StatusCreated {
		f.t.Fatalf("mint runner = %d", code)
	}
	auth := "Bearer " + m.Token
	var n claimedNode
	if code := f.call("POST", "/api/v1/runs/"+runID+"/nodes/compile/claim", auth,
		map[string]any{"holder_id": "pod-" + runID}, &n); code != http.StatusOK || n.RunID != runID {
		f.t.Fatalf("claim %s = %d %+v", runID, code, n)
	}
	return auth
}

func (f *appFixture) sourceToken(auth, runID string) (controller.SourceTokenResponse, int) {
	f.t.Helper()
	var out controller.SourceTokenResponse
	code := f.call("POST", "/api/v1/runs/"+runID+"/source-token", auth, nil, &out)
	return out, code
}

func TestGitHubAppSourceTokenReadsOnlyTheRunsRepository(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)

	runner := f.appWork(olga, "run-widgets", "acme/widgets")
	tok, code := f.sourceToken(runner, "run-widgets")
	if code != http.StatusOK || tok.Repository != "acme/widgets" {
		t.Fatalf("source token = %d %+v", code, tok)
	}
	if !f.app.TokenCovers(tok.Token, "acme/widgets") {
		t.Fatal("the token does not read the run's repository")
	}
	if f.app.TokenCovers(tok.Token, "acme/plans") {
		t.Fatal("the token reads acme/plans, another repository of the same installation")
	}
	minted := f.app.Minted()
	last := minted[len(minted)-1]
	if len(last.Repositories) != 1 || last.Repositories[0] != "widgets" || len(last.Permissions) != 1 || last.Permissions["contents"] != "read" {
		t.Fatalf("token request = %+v, want widgets with contents:read only", last)
	}

	other := f.appWork(olga, "run-plans", "acme/plans")
	if _, code := f.sourceToken(other, "run-widgets"); code != http.StatusForbidden {
		t.Fatalf("source token for a run the caller does not hold = %d, want 403", code)
	}
	unowned := f.appWork(olga, "run-elsewhere", "someone/else")
	if _, code := f.sourceToken(unowned, "run-elsewhere"); code != http.StatusNotFound {
		t.Fatalf("source token for an uncovered repository = %d, want 404", code)
	}
	bobs := f.appWork(bob, "run-bobs-widgets", "acme/widgets")
	if _, code := f.sourceToken(bobs, "run-bobs-widgets"); code != http.StatusNotFound {
		t.Fatalf("source token for another team's run of the repository = %d, want 404", code)
	}
	if _, code := f.sourceToken(runner, "run-bobs-widgets"); code != http.StatusNotFound && code != http.StatusForbidden {
		t.Fatalf("source token across teams = %d, want 404 or 403", code)
	}
}

func (f *appFixture) claimTrigger(owner signedIn, runID string) string {
	f.t.Helper()
	var m mintedRunner
	if code := f.call("POST", "/api/v1/team/runner-tokens", owner.auth,
		map[string]any{"name": "t-" + runID, "repos": []string{"github.com/*/*"}}, &m); code != http.StatusCreated {
		f.t.Fatalf("mint runner = %d", code)
	}
	auth := "Bearer " + m.Token
	if code := f.call("POST", "/api/v1/triggers/"+runID+"/claim", auth, nil, nil); code != http.StatusOK {
		f.t.Fatalf("claim trigger %s = %d", runID, code)
	}
	tn, err := f.store.ForTeam(context.Background(), store.Team(owner.team))
	if err != nil {
		f.t.Fatal(err)
	}
	now := time.Now()
	if err := tn.CreateRun(context.Background(), store.Run{
		ID: runID, Pipeline: "build", Status: "running", CreatedAt: now, StartedAt: now,
	}); err != nil {
		f.t.Fatalf("create run %s: %v", runID, err)
	}
	return auth
}

func TestGitHubAppForkPullRequestIsNotRun(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "build", map[string]any{"push": false, "pull_request": true, "fork_pull_requests": true}); code != http.StatusBadRequest {
		t.Fatalf("subscribing with fork_pull_requests = %d, want 400", code)
	}
	if code := f.subscribe(olga, "acme/widgets", "build", map[string]any{"push": false, "pull_request": true}); code != http.StatusOK {
		t.Fatalf("subscribe = %d", code)
	}
	if _, out := f.deliver("pull_request", prPayload(7, 701, "acme/widgets", 999), ""); out["status"] != "ignored" {
		t.Fatalf("fork PR = %v, want ignored", out)
	}
	gone := prPayload(7, 701, "acme/widgets", 0)
	gone["pull_request"].(map[string]any)["head"].(map[string]any)["repo"] = nil
	if _, out := f.deliver("pull_request", gone, ""); out["status"] != "ignored" {
		t.Fatalf("PR from a deleted head repository = %v, want ignored", out)
	}
	if n := len(f.triggers(olga.team)); n != 0 {
		t.Fatalf("fork PRs started %d runs", n)
	}
	if _, out := f.deliver("pull_request", prPayload(7, 701, "acme/widgets", 701), ""); out["status"] != "dispatched" {
		t.Fatalf("control: a same-repository PR = %v, want dispatched", out)
	}
}

func TestGitHubAppAdditionalEventsRequireSubscription(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	for _, ref := range []string{"refs/tags/v1-published", "refs/tags/v1-prereleased", "refs/heads/topic-create", "refs/heads/main"} {
		f.app.SetCommit("acme/widgets", ref, headSHA)
	}
	if code := f.subscribe(olga, "acme/widgets", "baseline", map[string]any{"push": false, "pull_request": true}); code != http.StatusOK {
		t.Fatalf("baseline subscription = %d", code)
	}
	if code := f.subscribe(olga, "acme/widgets", "opted", map[string]any{
		"push": false, "pull_request": false, "pull_request_closed": true,
		"pull_request_labeled": true, "pull_request_labels": []string{"ship"},
		"pull_request_ready_for_review": true, "release_published": true,
		"release_prereleased": true, "branch_create": true, "branch_delete": true,
	}); code != http.StatusOK {
		t.Fatalf("opt-in subscription = %d", code)
	}
	check := func(event string, payload map[string]any, want string) {
		t.Helper()
		_, out := f.deliver(event, payload, "")
		if out["status"] != want {
			t.Fatalf("%s delivery = %v, want %s", event, out, want)
		}
	}
	pr := prPayload(7, 701, "acme/widgets", 701)
	pr["action"] = "labeled"
	pr["label"] = map[string]any{"name": "skip"}
	check("pull_request", pr, "ignored")
	pr["action"] = "closed"
	check("pull_request", pr, "dispatched")
	pr["label"] = map[string]any{"name": "ship"}
	pr["action"] = "labeled"
	check("pull_request", pr, "dispatched")
	pr["action"] = "closed"
	pr["pull_request"].(map[string]any)["merged"] = true
	check("pull_request", pr, "dispatched")
	pr["action"] = "ready_for_review"
	check("pull_request", pr, "dispatched")
	pr["action"] = "opened"
	check("pull_request", pr, "dispatched")
	pr["action"] = "labeled"
	pr["pull_request"].(map[string]any)["head"].(map[string]any)["repo"] = map[string]any{"id": 999}
	check("pull_request", pr, "ignored")
	check("release", map[string]any{
		"action": "draft", "installation": map[string]any{"id": 7},
		"repository": map[string]any{"id": 701, "full_name": "acme/widgets"},
	}, "ignored")
	for _, action := range []string{"published", "prereleased"} {
		check("release", map[string]any{
			"action": action, "installation": map[string]any{"id": 7},
			"repository": map[string]any{"id": 701, "full_name": "acme/widgets"},
			"release":    map[string]any{"tag_name": "v1-" + action, "target_commitish": "main", "published_at": time.Now().UTC().Format(time.RFC3339)},
		}, "dispatched")
	}
	for _, event := range []string{"create", "delete"} {
		branch := map[string]any{
			"ref": "topic-" + event, "ref_type": "branch", "master_branch": "main",
			"installation": map[string]any{"id": 7},
			"repository":   map[string]any{"id": 701, "full_name": "acme/widgets", "pushed_at": time.Now().Unix()},
			"sender":       map[string]any{"login": "olga"},
		}
		check(event, branch, "dispatched")
		branch["ref_type"] = "tag"
		check(event, branch, "ignored")
	}
	got := f.triggers(olga.team)
	if len(got) != 9 {
		t.Fatalf("triggers = %d, want 9", len(got))
	}
	counts := map[string]int{}
	for _, tr := range got {
		event := tr.TriggerEnv["GITHUB_EVENT_NAME"]
		counts[event]++
		if tr.Pipeline == "baseline" && tr.TriggerEnv["GITHUB_EVENT_NAME"] != "pull_request" {
			t.Fatalf("baseline received %v", tr.TriggerEnv)
		}
		if tr.Pipeline == "opted" {
			ref := tr.TriggerEnv["GITHUB_REF"]
			if ref == "" || tr.TriggerEnv["GITHUB_REF_TYPE"] == "" || tr.GitSHA != headSHA {
				t.Fatalf("missing ref or resolved commit: %+v", tr)
			}
			switch event {
			case "pull_request":
				if ref != "refs/pull/12/head" || tr.TriggerEnv["GITHUB_ACTION"] == "" {
					t.Fatalf("PR env = %v", tr.TriggerEnv)
				}
				if tr.TriggerEnv["GITHUB_ACTION"] == "closed" && tr.TriggerEnv["GITHUB_MERGED"] == "" {
					t.Fatalf("closed PR env = %v", tr.TriggerEnv)
				}
			case "release":
				if !strings.HasPrefix(ref, "refs/tags/v1-") || tr.TriggerEnv["GITHUB_TAG_NAME"] == "" {
					t.Fatalf("release env = %v", tr.TriggerEnv)
				}
			case "create", "delete":
				if !strings.HasPrefix(ref, "refs/heads/topic-") {
					t.Fatalf("branch env = %v", tr.TriggerEnv)
				}
			}
		}
	}
	if counts["pull_request"] != 5 || counts["release"] != 2 || counts["create"] != 1 || counts["delete"] != 1 {
		t.Fatalf("event counts = %v", counts)
	}
}

func TestGitHubAppRepositoryIdentitySurvivesRenameAndTransfer(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "build", nil); code != http.StatusOK {
		t.Fatal(code)
	}
	f.app.SetRepos(7, githubapptest.Repo{ID: 999, FullName: "acme/gadgets"})
	if _, out := f.deliver("repository", map[string]any{
		"action": "renamed", "installation": map[string]any{"id": 7},
		"repository": map[string]any{"id": 701, "full_name": "acme/gadgets"},
	}, ""); out["status"] != "ignored" {
		t.Fatalf("rename to a different repository id = %v", out)
	}
	f.app.SetRepos(7, githubapptest.Repo{ID: 701, FullName: "acme/gadgets"})
	if code, out := f.deliver("repository", map[string]any{
		"action": "renamed", "installation": map[string]any{"id": 7},
		"repository": map[string]any{"id": 701, "full_name": "acme/gadgets"},
	}, ""); code != http.StatusOK || out["status"] != "updated" {
		t.Fatalf("rename = %d %v", code, out)
	}
	if _, out := f.deliver("push", pushPayload(7, 701, "acme/gadgets", headSHA), ""); out["status"] != "dispatched" {
		t.Fatalf("push after rename = %v", out)
	}
	f.app.AddInstallation(githubapptest.Installation{
		ID: 9, Account: githubapp.Account{ID: 90, Login: "other", Type: "Organization"},
		Repos: []githubapptest.Repo{{ID: 701, FullName: "other/gadgets"}},
	})
	f.connect(olga, 501, 9, map[string]githubapp.OrgMembership{"other": {State: "active", Role: "admin"}})
	f.app.SetRepos(7)
	if code, out := f.deliver("repository", map[string]any{
		"action": "transferred", "installation": map[string]any{"id": 9},
		"repository": map[string]any{"id": 701, "full_name": "other/gadgets"},
	}, ""); code != http.StatusOK || out["status"] != "updated" {
		t.Fatalf("transfer = %d %v", code, out)
	}
	if _, out := f.deliver("push", pushPayload(9, 701, "other/gadgets", headSHA), ""); out["status"] != "dispatched" {
		t.Fatalf("push after transfer = %v", out)
	}
	got := f.triggers(olga.team)
	if len(got) != 2 {
		t.Fatalf("runs after repository changes = %d, want 2", len(got))
	}
	for _, tr := range got {
		if tr.Repo != "acme/gadgets" && tr.Repo != "other/gadgets" {
			t.Fatalf("unexpected repository %q", tr.Repo)
		}
	}
}

func TestGitHubAppReleaseReplayAndCoverage(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "release", map[string]any{"push": false, "release_published": true}); code != http.StatusOK {
		t.Fatal(code)
	}
	f.app.SetCommit("acme/widgets", "refs/tags/v1", headSHA)
	payload := map[string]any{
		"action": "published", "installation": map[string]any{"id": 7},
		"repository": map[string]any{"id": 701, "full_name": "acme/widgets"},
		"release":    map[string]any{"tag_name": "v1", "published_at": time.Now().UTC().Format(time.RFC3339)},
	}
	if _, out := f.deliver("release", payload, ""); out["status"] != "dispatched" {
		t.Fatalf("release = %v", out)
	}
	f.app.SetCommit("acme/widgets", "refs/tags/v1", "")
	f.app.SetRepos(7)
	if _, out := f.deliver("release", payload, ""); out["status"] != "duplicate" {
		t.Fatalf("redelivery = %v", out)
	}
	payload["release"].(map[string]any)["tag_name"] = "v2"
	if _, out := f.deliver("release", payload, ""); out["status"] != "ignored" {
		t.Fatalf("release after installation lost repository = %v", out)
	}
	if got := f.triggers(olga.team); len(got) != 1 {
		t.Fatalf("replay and uncovered release started %d runs", len(got))
	}
}

func TestGitHubAppPushOfNoCommitIsIgnored(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	if _, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", strings.Repeat("0", 40)), ""); out["status"] != "ignored" {
		t.Fatalf("push to the zero commit = %v, want ignored", out)
	}
	if n := len(f.triggers(olga.team)); n != 0 {
		t.Fatalf("a push of no commit started %d runs", n)
	}
}

// An event whose time cannot be read cannot be held to the binding's age, so
// it starts nothing.
func TestGitHubAppUndatedEventIsIgnored(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", map[string]any{"push": true, "pull_request": true})
	push := pushPayload(7, 701, "acme/widgets", headSHA)
	delete(push["repository"].(map[string]any), "pushed_at")
	if _, out := f.deliver("push", push, ""); out["status"] != "ignored" {
		t.Fatalf("push without pushed_at = %v, want ignored", out)
	}
	pr := prPayload(7, 701, "acme/widgets", 701)
	pr["pull_request"].(map[string]any)["updated_at"] = "yesterday"
	if _, out := f.deliver("pull_request", pr, ""); out["status"] != "ignored" {
		t.Fatalf("pull request with an unreadable updated_at = %v, want ignored", out)
	}
	if n := len(f.triggers(olga.team)); n != 0 {
		t.Fatalf("undated events started %d runs", n)
	}
	if _, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), ""); out["status"] != "dispatched" {
		t.Fatalf("control: a dated push = %v, want dispatched", out)
	}
}

// A redelivery of a partly shed delivery spends allowance only on the runs it
// did not start the first time.
func TestGitHubAppRedeliveryChargesOnlyShedRuns(t *testing.T) {
	f := newAppFixture(t)
	f.srv.WithFloodPolicy(controller.FloodPolicy{RunsPerPrincipalHour: 1})
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	f.subscribe(olga, "acme/widgets", "test", nil)
	push := pushPayload(7, 701, "acme/widgets", headSHA)
	if _, out := f.deliver("push", push, ""); !slices.Equal(runStatuses(out), []string{"dispatched", "shed"}) {
		t.Fatalf("first delivery = %v, want one run and one shed", out)
	}
	f.srv.WithFloodPolicy(controller.FloodPolicy{RunsPerPrincipalHour: 1})
	if code, out := f.deliver("push", push, ""); code != http.StatusAccepted || !slices.Equal(runStatuses(out), []string{"duplicate", "dispatched"}) {
		t.Fatalf("redelivery with one run of allowance = %d %v, want the shed run started", code, out)
	}
	if n := len(f.triggers(olga.team)); n != 2 {
		t.Fatalf("the team has %d runs, want 2", n)
	}
}

func TestGitHubAppPartialReleaseRedeliveryKeepsResolvedCommit(t *testing.T) {
	f := newAppFixture(t)
	f.srv.WithFloodPolicy(controller.FloodPolicy{RunsPerPrincipalHour: 1})
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	for _, pipeline := range []string{"build", "test"} {
		if code := f.subscribe(olga, "acme/widgets", pipeline, map[string]any{"push": false, "release_published": true}); code != http.StatusOK {
			t.Fatal(code)
		}
	}
	f.app.SetCommit("acme/widgets", "refs/tags/v1", headSHA)
	payload := map[string]any{
		"action": "published", "installation": map[string]any{"id": 7},
		"repository": map[string]any{"id": 701, "full_name": "acme/widgets"},
		"release":    map[string]any{"tag_name": "v1", "published_at": time.Now().UTC().Format(time.RFC3339)},
	}
	if _, out := f.deliver("release", payload, ""); !slices.Equal(runStatuses(out), []string{"dispatched", "shed"}) {
		t.Fatalf("first release = %v", out)
	}
	f.app.SetCommit("acme/widgets", "refs/tags/v1", strings.Repeat("a", 40))
	f.srv.WithFloodPolicy(controller.FloodPolicy{RunsPerPrincipalHour: 1})
	if _, out := f.deliver("release", payload, ""); !slices.Equal(runStatuses(out), []string{"duplicate", "dispatched"}) {
		t.Fatalf("release redelivery = %v", out)
	}
	for _, tr := range f.triggers(olga.team) {
		if tr.GitSHA != headSHA {
			t.Fatalf("redelivery changed release commit to %s", tr.GitSHA)
		}
	}
}

func runStatuses(out map[string]any) []string {
	runs, _ := out["runs"].([]any)
	var got []string
	for _, r := range runs {
		got = append(got, r.(map[string]any)["status"].(string))
	}
	return got
}

// The hourly cap binds the team, counts each run a delivery creates, and is
// not spent by a redelivery.
func TestGitHubAppFloodCapCountsTheTeamsRuns(t *testing.T) {
	f := newAppFixture(t)
	f.srv.WithFloodPolicy(controller.FloodPolicy{RunsPerPrincipalHour: 3})
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	for _, sub := range [][2]string{{"acme/widgets", "build"}, {"acme/widgets", "test"}, {"acme/plans", "build"}, {"acme/plans", "test"}} {
		if code := f.subscribe(olga, sub[0], sub[1], nil); code != http.StatusOK {
			t.Fatalf("subscribe %v = %d", sub, code)
		}
	}
	widgets := pushPayload(7, 701, "acme/widgets", headSHA)
	if code, out := f.deliver("push", widgets, ""); code != http.StatusAccepted || !slices.Equal(runStatuses(out), []string{"dispatched", "dispatched"}) {
		t.Fatalf("first push = %d %v", code, out)
	}
	if code, out := f.deliver("push", widgets, ""); code == http.StatusTooManyRequests || !slices.Equal(runStatuses(out), []string{"duplicate", "duplicate"}) {
		t.Fatalf("redelivery = %d %v, want duplicates that spend nothing", code, out)
	}
	code, out := f.deliver("push", pushPayload(7, 702, "acme/plans", headSHA), "")
	if code != http.StatusAccepted || !slices.Equal(runStatuses(out), []string{"dispatched", "shed"}) {
		t.Fatalf("push to another repository = %d %v, want one run and one shed", code, out)
	}
	if code, _ := f.deliver("push", pushPayload(7, 702, "acme/plans", strings.Repeat("2", 40)), ""); code != http.StatusTooManyRequests {
		t.Fatalf("push past the team's cap = %d, want 429", code)
	}
	if n := len(f.triggers(olga.team)); n != 3 {
		t.Fatalf("the team has %d runs, want the cap of 3", n)
	}
}

func pushedAt(p map[string]any, at time.Time) map[string]any {
	p["repository"].(map[string]any)["pushed_at"] = at.Unix()
	return p
}

// After the operator moves an installation, a delivery the old team already
// ran, or any event from before the move, starts nothing in the new team.
func TestGitHubAppReplayAfterAMoveStartsNothing(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	old := pushedAt(pushPayload(7, 701, "acme/widgets", headSHA), time.Now())
	if _, out := f.deliver("push", old, ""); out["status"] != "dispatched" {
		t.Fatalf("olga's push = %v", out)
	}
	unsent := pushedAt(pushPayload(7, 701, "acme/widgets", strings.Repeat("3", 40)), time.Now().Add(-time.Hour))
	if code := f.call("DELETE", "/api/v1/github-app/installations/7", "Bearer "+f.admin, nil, nil); code != http.StatusNoContent {
		t.Fatalf("operator unbind = %d", code)
	}
	f.connect(bob, 502, 7, acmeAdmin)
	f.subscribe(bob, "acme/widgets", "build", nil)

	if _, out := f.deliver("push", old, ""); out["status"] == "dispatched" {
		t.Fatalf("replay of olga's delivery in bob's team = %v", out)
	}
	if _, out := f.deliver("push", unsent, ""); out["status"] != "ignored" {
		t.Fatalf("an event from before bob connected = %v, want ignored", out)
	}
	if n := len(f.triggers(bob.team)); n != 0 {
		t.Fatalf("bob's team got %d runs from events before it held the installation", n)
	}
	fresh := pushedAt(pushPayload(7, 701, "acme/widgets", strings.Repeat("4", 40)), time.Now())
	if _, out := f.deliver("push", fresh, ""); out["status"] != "dispatched" {
		t.Fatalf("control: a push after bob connected = %v, want dispatched", out)
	}
}

// A repository removed from the installation on GitHub starts nothing, even
// though the team's subscription to it remains.
func TestGitHubAppRunNeedsTheRepositoryStillCovered(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.subscribe(olga, "acme/widgets", "build", nil)
	// The subscription read GitHub's answer moments ago, and no
	// installation_repositories delivery arrives to drop it.
	f.app.SetRepos(7, githubapptest.Repo{ID: 702, FullName: "acme/plans", Private: true})
	if _, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), ""); out["status"] != "ignored" {
		t.Fatalf("push to a repository the installation no longer covers = %v, want ignored", out)
	}
	if n := len(f.triggers(olga.team)); n != 0 {
		t.Fatalf("an uncovered repository started %d runs", n)
	}
}

// A runner that asks for its run's source token in a loop gets the one live
// token back, and a claim that keeps asking is refused.
func TestGitHubAppSourceTokenIsMintedOncePerClaim(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	runner := f.appWork(olga, "run-widgets", "acme/widgets")
	before := len(f.app.Minted())
	first, code := f.sourceToken(runner, "run-widgets")
	if code != http.StatusOK {
		t.Fatalf("source token = %d", code)
	}
	for i := 0; i < 5; i++ {
		again, code := f.sourceToken(runner, "run-widgets")
		if code != http.StatusOK || again.Token != first.Token {
			t.Fatalf("ask %d = %d %q, want the first token again", i, code, again.Token)
		}
	}
	if n := len(f.app.Minted()) - before; n != 1 {
		t.Fatalf("six asks minted %d tokens, want 1", n)
	}
	refused := false
	for i := 0; i < 10 && !refused; i++ {
		_, code := f.sourceToken(runner, "run-widgets")
		refused = code == http.StatusTooManyRequests
	}
	if !refused {
		t.Fatal("a claim asking in a loop was never refused")
	}
}

func TestGitHubAppBranchFiltersGateBranchAndPullRequestEvents(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	for _, ref := range []string{"refs/heads/main", "refs/heads/release", "refs/heads/topic"} {
		f.app.SetCommit("acme/widgets", ref, headSHA)
	}
	if code := f.subscribe(olga, "acme/widgets", "deploy", map[string]any{
		"push": false, "branch_create": true, "branch_delete": true, "pull_request_closed": true,
		"branches": []string{"main", "release"}, "base_branches": []string{"main"},
	}); code != http.StatusOK {
		t.Fatalf("filtered subscription = %d", code)
	}
	for _, tc := range []struct {
		event, ref, want string
	}{
		{"create", "topic", "ignored"},
		{"create", "release", "dispatched"},
		{"delete", "topic", "ignored"},
		{"delete", "release", "dispatched"},
	} {
		payload := map[string]any{
			"ref": tc.ref, "ref_type": "branch", "master_branch": "main",
			"installation": map[string]any{"id": 7},
			"repository":   map[string]any{"id": 701, "full_name": "acme/widgets", "pushed_at": time.Now().Unix()},
			"sender":       map[string]any{"login": "olga"},
			"pusher_type":  tc.event,
		}
		if _, out := f.deliver(tc.event, payload, ""); out["status"] != tc.want {
			t.Fatalf("%s %s = %v, want %s", tc.event, tc.ref, out, tc.want)
		}
	}
	pr := prPayload(7, 701, "acme/widgets", 701)
	pr["action"] = "closed"
	pr["pull_request"].(map[string]any)["base"].(map[string]any)["ref"] = "topic"
	if _, out := f.deliver("pull_request", pr, ""); out["status"] != "ignored" {
		t.Fatalf("closed PR into an unlisted base = %v", out)
	}
	pr["pull_request"].(map[string]any)["base"].(map[string]any)["ref"] = "main"
	if _, out := f.deliver("pull_request", pr, ""); out["status"] != "dispatched" {
		t.Fatalf("closed PR into main = %v", out)
	}
	if n := len(f.triggers(olga.team)); n != 3 {
		t.Fatalf("branch filters started %d runs, want 3", n)
	}
}

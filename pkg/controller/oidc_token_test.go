package controller_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/jwks"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type oidcFixture struct {
	st     *store.Store
	acme   *store.Tenant
	globex *store.Tenant
	now    time.Time
}

func newOIDCFixture(t *testing.T) *oidcFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &oidcFixture{st: st, now: time.Now().UTC()}
	for _, team := range []store.Team{"acme", "globex"} {
		if err := st.AsOperator().CreateTeam(ctx, team); err != nil {
			t.Fatal(err)
		}
	}
	if f.acme, err = st.ForTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if f.globex, err = st.ForTeam(ctx, "globex"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *oidcFixture) run(t *testing.T, tn *store.Tenant, trig store.Trigger) {
	t.Helper()
	trig.CreatedAt = f.now
	if err := tn.CreateTriggerWithRun(context.Background(), trig, store.Run{
		ID: trig.ID, Pipeline: trig.Pipeline, Status: "pending", StartedAt: f.now,
		TriggerSource: trig.TriggerSource, GitBranch: trig.GitBranch, GitSHA: trig.GitSHA,
		GithubOwner: trig.GithubOwner, GithubRepo: trig.GithubRepo, RepoURL: trig.RepoURL,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *oidcFixture) runner(t *testing.T, tn *store.Tenant, name string) string {
	t.Helper()
	raw, _, err := tn.CreateToken(context.Background(), name, store.TokenKindRunner, runnerScopes, 0, f.now)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func oidcKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// hack: the issuer is the server's own URL, known only once the listener exists.
func serveOIDC(t *testing.T, st *store.Store, active, published []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	c := controller.New(st, nil).EnableAuthFromStore()
	if active != nil {
		iss, err := oidcissuer.New("http://"+srv.Listener.Addr().String(), active, published, 0)
		if err != nil {
			t.Fatal(err)
		}
		c = c.WithOIDCIssuer(iss)
	}
	srv.Config.Handler = c.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

type runTokenClaims struct {
	Iss        string        `json:"iss"`
	Aud        jwks.Audience `json:"aud"`
	Sub        string        `json:"sub"`
	Iat        int64         `json:"iat"`
	Nbf        int64         `json:"nbf"`
	Exp        int64         `json:"exp"`
	Jti        string        `json:"jti"`
	Team       string        `json:"team"`
	Pipeline   string        `json:"pipeline"`
	Trigger    string        `json:"trigger"`
	RunnerKind string        `json:"runner_kind"`
	Ref        string        `json:"ref"`
	SHA        string        `json:"sha"`
	Repository string        `json:"repository"`
	RunID      string        `json:"run_id"`
}

func verifyAgainst(t *testing.T, srv *httptest.Server, token string) (runTokenClaims, error) {
	t.Helper()
	var claims runTokenClaims
	err := jwks.New(srv.URL+oidcissuer.JWKSPath, srv.Client(), nil).Verify(context.Background(), token, &claims)
	return claims, err
}

// A runner holding a push run's trigger claim gets a token that verifies
// against the key set the controller serves and names the run.
func TestOIDCTokenVerifiesAndNamesTheClaimedRun(t *testing.T) {
	ctx := context.Background()
	f := newOIDCFixture(t)
	f.run(t, f.acme, store.Trigger{
		ID: "run-hook", Pipeline: "deploy", TriggerSource: "github",
		GitBranch: "main", GitSHA: "0123456789abcdef0123456789abcdef01234567",
		GithubOwner: "acme", GithubRepo: "api",
		TriggerEnv: map[string]string{sparkwing.EnvGitHubEventName: "push"},
	})
	raw := f.runner(t, f.acme, "agent:acme")
	srv := serveOIDC(t, f.st, oidcKeyPEM(t), nil)
	c := client.NewWithToken(srv.URL, nil, raw)
	if _, err := c.ClaimSpecificTrigger(ctx, "run-hook", time.Minute); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}
	before := time.Now().Add(-time.Second).Unix()
	tok, err := c.OIDCToken(ctx, "run-hook", "sts.amazonaws.com")
	if err != nil {
		t.Fatalf("OIDCToken: %v", err)
	}
	claims, err := verifyAgainst(t, srv, tok.Token)
	if err != nil {
		t.Fatalf("the token does not verify against the served key set: %v", err)
	}
	if claims.Iss != srv.URL {
		t.Errorf("iss = %q, want the issuer %q exactly", claims.Iss, srv.URL)
	}
	if len(claims.Aud) != 1 || claims.Aud[0] != "sts.amazonaws.com" {
		t.Errorf("aud = %v, want [sts.amazonaws.com]", claims.Aud)
	}
	if want := "team:acme:pipeline:deploy:trigger:push:runner:runner:ref:refs/heads/main"; claims.Sub != want {
		t.Errorf("sub = %q, want %q", claims.Sub, want)
	}
	got := [...]string{claims.Team, claims.Pipeline, claims.Trigger, claims.RunnerKind, claims.Ref, claims.SHA, claims.Repository, claims.RunID}
	want := [...]string{"acme", "deploy", "push", "runner", "refs/heads/main", "0123456789abcdef0123456789abcdef01234567", "github.com/acme/api", "run-hook"}
	if got != want {
		t.Errorf("custom claims = %v, want %v", got, want)
	}
	if claims.Iat < before || claims.Nbf != claims.Iat || claims.Exp != claims.Iat+int64(oidcissuer.DefaultTTL/time.Second) {
		t.Errorf("iat/nbf/exp = %d/%d/%d, want nbf = iat and exp = iat + 10m", claims.Iat, claims.Nbf, claims.Exp)
	}
	if !tok.ExpiresAt.Equal(time.Unix(claims.Exp, 0)) {
		t.Errorf("expires_at = %s, want the exp claim %s", tok.ExpiresAt, time.Unix(claims.Exp, 0))
	}
	again, err := c.OIDCToken(ctx, "run-hook", "sts.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := verifyAgainst(t, srv, again.Token)
	if err != nil || second.Jti == "" || second.Jti == claims.Jti {
		t.Errorf("jti = %q then %q (err %v), want two distinct ids", claims.Jti, second.Jti, err)
	}
}

// The trigger claim reports cron only for the controller's own schedule,
// whose marker a submitter cannot set, and manual for everything else.
func TestOIDCTokenTriggerComesFromTheIntakeRow(t *testing.T) {
	ctx := context.Background()
	f := newOIDCFixture(t)
	f.run(t, f.acme, store.Trigger{
		ID: "run-cron", Pipeline: "nightly", TriggerSource: "schedule",
		TriggerEnv: map[string]string{crons.ScheduleEnvKey: "sched-1"}, GitBranch: "main",
		RepoURL: "git@github.com:Acme/API.git",
	})
	f.run(t, f.acme, store.Trigger{ID: "run-fake-cron", Pipeline: "nightly", TriggerSource: "schedule"})
	f.run(t, f.acme, store.Trigger{ID: "run-cli", Pipeline: "nightly", TriggerSource: "cli"})
	raw := f.runner(t, f.acme, "agent:acme")
	srv := serveOIDC(t, f.st, oidcKeyPEM(t), nil)
	c := client.NewWithToken(srv.URL, nil, raw)
	for _, tc := range []struct{ run, trigger, sub, repo string }{
		{"run-cron", "cron", "team:acme:pipeline:nightly:trigger:cron:runner:runner:ref:refs/heads/main", "github.com/Acme/API"},
		{"run-fake-cron", "manual", "team:acme:pipeline:nightly:trigger:manual:runner:runner:ref:", ""},
		{"run-cli", "manual", "team:acme:pipeline:nightly:trigger:manual:runner:runner:ref:", ""},
	} {
		if _, err := c.ClaimSpecificTrigger(ctx, tc.run, time.Minute); err != nil {
			t.Fatalf("%s: ClaimSpecificTrigger: %v", tc.run, err)
		}
		tok, err := c.OIDCToken(ctx, tc.run, "vault")
		if err != nil {
			t.Fatalf("%s: OIDCToken: %v", tc.run, err)
		}
		claims, err := verifyAgainst(t, srv, tok.Token)
		if err != nil {
			t.Fatal(err)
		}
		if claims.Trigger != tc.trigger || claims.Sub != tc.sub || claims.Repository != tc.repo {
			t.Errorf("%s: trigger/sub/repository = %q/%q/%q, want %q/%q/%q",
				tc.run, claims.Trigger, claims.Sub, claims.Repository, tc.trigger, tc.sub, tc.repo)
		}
	}
}

// Only the claim holder gets a token. A same-team runner with no claim and
// a runner from another team both get the same 404 a missing run gets.
func TestOIDCTokenRefusesAnyoneButTheClaimHolder(t *testing.T) {
	ctx := context.Background()
	f := newOIDCFixture(t)
	f.run(t, f.acme, store.Trigger{ID: "run-acme", Pipeline: "deploy", TriggerSource: "github", GitBranch: "main"})
	f.run(t, f.globex, store.Trigger{ID: "run-globex", Pipeline: "deploy", TriggerSource: "github", GitBranch: "main"})
	holderRaw, bystanderRaw, globexRaw := f.runner(t, f.acme, "agent:acme"), f.runner(t, f.acme, "agent:acme-2"), f.runner(t, f.globex, "agent:globex")
	srv := serveOIDC(t, f.st, oidcKeyPEM(t), nil)
	holder := client.NewWithToken(srv.URL, nil, holderRaw)
	if _, err := holder.ClaimSpecificTrigger(ctx, "run-acme", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.OIDCToken(ctx, "run-acme", "sts.amazonaws.com"); err != nil {
		t.Fatalf("control: the claim holder was refused: %v", err)
	}
	bystander := client.NewWithToken(srv.URL, nil, bystanderRaw)
	globex := client.NewWithToken(srv.URL, nil, globexRaw)
	if _, err := globex.ClaimSpecificTrigger(ctx, "run-globex", time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		c    *client.Client
		run  string
	}{
		{"same-team runner without the claim", bystander, "run-acme"},
		{"another team's runner", globex, "run-acme"},
		{"the holder naming another team's run", holder, "run-globex"},
		{"the holder naming no run", holder, "run-missing"},
	} {
		_, err := tc.c.OIDCToken(ctx, tc.run, "sts.amazonaws.com")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want 404", tc.name, err)
		}
	}
	if _, err := globex.OIDCToken(ctx, "run-globex", "sts.amazonaws.com"); err != nil {
		t.Errorf("control: globex's own claim holder was refused: %v", err)
	}
}

func postOIDCToken(t *testing.T, srv *httptest.Server, token, runID, body string) (status int, retryAfter string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/runs/"+runID+"/oidc-token", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Retry-After")
}

func TestOIDCTokenValidatesTheRequestAndTheSubject(t *testing.T) {
	ctx := context.Background()
	f := newOIDCFixture(t)
	f.run(t, f.acme, store.Trigger{ID: "run-ok", Pipeline: "deploy", TriggerSource: "cli", GitBranch: "main"})
	f.run(t, f.acme, store.Trigger{ID: "run-forged", Pipeline: "deploy:trigger:push", TriggerSource: "cli"})
	raw := f.runner(t, f.acme, "agent:acme")
	srv := serveOIDC(t, f.st, oidcKeyPEM(t), nil)
	c := client.NewWithToken(srv.URL, nil, raw)
	for _, id := range []string{"run-ok", "run-forged"} {
		if _, err := c.ClaimSpecificTrigger(ctx, id, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		run, body string
		want      int
	}{
		{"run-ok", `{"audience":"sts.amazonaws.com"}`, http.StatusOK},
		{"run-ok", `{}`, http.StatusBadRequest},
		{"run-ok", `{"audience":"two words"}`, http.StatusBadRequest},
		{"run-ok", `{"audience":"` + strings.Repeat("a", 257) + `"}`, http.StatusBadRequest},
		{"run-forged", `{"audience":"sts.amazonaws.com"}`, http.StatusUnprocessableEntity},
	} {
		if got, _ := postOIDCToken(t, srv, raw, tc.run, tc.body); got != tc.want {
			t.Errorf("%s %s: status %d, want %d", tc.run, tc.body, got, tc.want)
		}
	}
}

func TestOIDCTokenIsRateLimitedPerClaim(t *testing.T) {
	ctx := context.Background()
	f := newOIDCFixture(t)
	f.run(t, f.acme, store.Trigger{ID: "run-1", Pipeline: "deploy", TriggerSource: "cli"})
	raw := f.runner(t, f.acme, "agent:acme")
	srv := serveOIDC(t, f.st, oidcKeyPEM(t), nil)
	if _, err := client.NewWithToken(srv.URL, nil, raw).ClaimSpecificTrigger(ctx, "run-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if got, _ := postOIDCToken(t, srv, raw, "run-1", `{"audience":"a"}`); got != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200 within the budget", i+1, got)
		}
	}
	status, retryAfter := postOIDCToken(t, srv, raw, "run-1", `{"audience":"a"}`)
	if status != http.StatusTooManyRequests || retryAfter == "" {
		t.Fatalf("request 31: status %d Retry-After %q, want 429 with a Retry-After", status, retryAfter)
	}
}

// A controller with no signing key answers 404 on all three routes, and
// its discovery routes need no credential when it has one.
func TestOIDCRoutesWithAndWithoutAKey(t *testing.T) {
	ctx := context.Background()
	f := newOIDCFixture(t)
	f.run(t, f.acme, store.Trigger{ID: "run-1", Pipeline: "deploy", TriggerSource: "cli"})
	raw := f.runner(t, f.acme, "agent:acme")
	off := serveOIDC(t, f.st, nil, nil)
	c := client.NewWithToken(off.URL, nil, raw)
	if _, err := c.ClaimSpecificTrigger(ctx, "run-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OIDCToken(ctx, "run-1", "a"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("token with no key: err = %v, want 404", err)
	}
	on := serveOIDC(t, f.st, oidcKeyPEM(t), nil)
	for _, tc := range []struct {
		srv  *httptest.Server
		path string
		want int
	}{
		{off, oidcissuer.DiscoveryPath, http.StatusNotFound},
		{off, oidcissuer.JWKSPath, http.StatusNotFound},
		{on, oidcissuer.DiscoveryPath, http.StatusOK},
		{on, oidcissuer.JWKSPath, http.StatusOK},
	} {
		resp, err := tc.srv.Client().Get(tc.srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("GET %s: status %d, want %d", tc.path, resp.StatusCode, tc.want)
		}
		if tc.want == http.StatusOK && !strings.Contains(resp.Header.Get("Cache-Control"), "max-age=") {
			t.Errorf("GET %s: Cache-Control %q, want a max-age", tc.path, resp.Header.Get("Cache-Control"))
		}
	}
	resp, err := on.Client().Get(on.URL + oidcissuer.DiscoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var doc oidcissuer.Discovery
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Issuer != on.URL || doc.JWKSURI != on.URL+oidcissuer.JWKSPath {
		t.Errorf("issuer/jwks_uri = %q/%q, want %q and its key set", doc.Issuer, doc.JWKSURI, on.URL)
	}
}

// The two-step rotation: publish the next key while the old one signs, then
// switch signing and publish the old key. A key set cached at either step
// verifies every token issued across the switch. Each controller below is
// the same deployment restarted with new key flags.
func TestOIDCKeyRotation(t *testing.T) {
	ctx := context.Background()
	f := newOIDCFixture(t)
	f.run(t, f.acme, store.Trigger{ID: "run-1", Pipeline: "deploy", TriggerSource: "cli"})
	oldKey, newKey := oidcKeyPEM(t), oidcKeyPEM(t)
	raw := f.runner(t, f.acme, "agent:acme")
	before := serveOIDC(t, f.st, oldKey, nil)
	c := client.NewWithToken(before.URL, nil, raw)
	if _, err := c.ClaimSpecificTrigger(ctx, "run-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	old, err := c.OIDCToken(ctx, "run-1", "sts.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	prepublish := serveOIDC(t, f.st, oldKey, newKey)
	staged, err := client.NewWithToken(prepublish.URL, nil, raw).OIDCToken(ctx, "run-1", "sts.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAgainst(t, before, staged.Token); err != nil {
		t.Errorf("publishing the next key changed the signing key: %v", err)
	}
	during := serveOIDC(t, f.st, newKey, oldKey)
	if _, err := verifyAgainst(t, during, old.Token); err != nil {
		t.Errorf("an old-key token failed while the old key is published: %v", err)
	}
	fresh, err := client.NewWithToken(during.URL, nil, raw).OIDCToken(ctx, "run-1", "sts.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAgainst(t, prepublish, fresh.Token); err != nil {
		t.Errorf("a key set cached before the switch rejected a new-key token: %v", err)
	}
	if _, err := verifyAgainst(t, before, fresh.Token); !errors.Is(err, jwks.ErrRejected) {
		t.Errorf("control: a key set that never held the new key: err = %v, want rejected", err)
	}
	if _, err := verifyAgainst(t, during, fresh.Token); err != nil {
		t.Errorf("a new-key token failed during the rotation: %v", err)
	}
	after := serveOIDC(t, f.st, newKey, nil)
	if _, err := verifyAgainst(t, after, old.Token); !errors.Is(err, jwks.ErrRejected) {
		t.Errorf("an old-key token after its key was dropped: err = %v, want rejected", err)
	}
	if _, err := verifyAgainst(t, after, fresh.Token); err != nil {
		t.Errorf("control: a new-key token failed after the rotation: %v", err)
	}
}

// jwks.Verify proves the signature only, so a relying party also checks
// exp; this pins that a token's exp is in the past once its lifetime ends
// and that a tampered payload stops verifying.
func TestOIDCTokenExpiryAndTampering(t *testing.T) {
	ctx := context.Background()
	f := newOIDCFixture(t)
	f.run(t, f.acme, store.Trigger{ID: "run-1", Pipeline: "deploy", TriggerSource: "cli"})
	raw := f.runner(t, f.acme, "agent:acme")
	srv := serveOIDC(t, f.st, oidcKeyPEM(t), nil)
	c := client.NewWithToken(srv.URL, nil, raw)
	if _, err := c.ClaimSpecificTrigger(ctx, "run-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	tok, err := c.OIDCToken(ctx, "run-1", "sts.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyAgainst(t, srv, tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if later := time.Now().Add(oidcissuer.DefaultTTL + time.Second).Unix(); claims.Exp >= later {
		t.Errorf("exp %d is not before %d, one lifetime from now", claims.Exp, later)
	}
	parts := strings.Split(tok.Token, ".")
	var body map[string]any
	if err := jwks.DecodeSegment(parts[1], &body); err != nil {
		t.Fatal(err)
	}
	body["team"] = "globex"
	forged, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(forged)
	if _, err := verifyAgainst(t, srv, strings.Join(parts, ".")); !errors.Is(err, jwks.ErrRejected) {
		t.Errorf("a token with a rewritten team claim: err = %v, want rejected", err)
	}
}

// A same-repository pull request from a branch named main records main as
// its run branch; it must still never carry the subject a push to main gets.
func TestOIDCTokenPullRequestNeverLooksLikeAPush(t *testing.T) {
	ctx := context.Background()
	f := newOIDCFixture(t)
	f.run(t, f.acme, store.Trigger{
		ID: "run-pr", Pipeline: "deploy", TriggerSource: "github", GitBranch: "main",
		GithubOwner: "acme", GithubRepo: "api",
		TriggerEnv: map[string]string{sparkwing.EnvGitHubEventName: sparkwing.EventPullRequest, sparkwing.EnvPRNumber: "7"},
	})
	f.run(t, f.acme, store.Trigger{
		ID: "run-push", Pipeline: "deploy", TriggerSource: "github", GitBranch: "main",
		GithubOwner: "acme", GithubRepo: "api",
		TriggerEnv: map[string]string{sparkwing.EnvGitHubEventName: "push"},
	})
	f.run(t, f.acme, store.Trigger{ID: "run-no-event", Pipeline: "deploy", TriggerSource: "github", GitBranch: "main"})
	raw := f.runner(t, f.acme, "agent:acme")
	srv := serveOIDC(t, f.st, oidcKeyPEM(t), nil)
	c := client.NewWithToken(srv.URL, nil, raw)
	for _, tc := range []struct{ run, sub, ref string }{
		{"run-pr", "team:acme:pipeline:deploy:trigger:pull_request:runner:runner:ref:refs/pull/7/head", "refs/pull/7/head"},
		{"run-push", "team:acme:pipeline:deploy:trigger:push:runner:runner:ref:refs/heads/main", "refs/heads/main"},
		{"run-no-event", "team:acme:pipeline:deploy:trigger:manual:runner:runner:ref:refs/heads/main", "refs/heads/main"},
	} {
		if _, err := c.ClaimSpecificTrigger(ctx, tc.run, time.Minute); err != nil {
			t.Fatalf("%s: ClaimSpecificTrigger: %v", tc.run, err)
		}
		tok, err := c.OIDCToken(ctx, tc.run, "sts.amazonaws.com")
		if err != nil {
			t.Fatalf("%s: OIDCToken: %v", tc.run, err)
		}
		claims, err := verifyAgainst(t, srv, tok.Token)
		if err != nil {
			t.Fatal(err)
		}
		if claims.Sub != tc.sub || claims.Ref != tc.ref {
			t.Errorf("%s: sub/ref = %q/%q, want %q/%q", tc.run, claims.Sub, claims.Ref, tc.sub, tc.ref)
		}
	}
}

func TestOIDCTokenTagPushNamesTagRef(t *testing.T) {
	ctx := context.Background()
	f := newOIDCFixture(t)
	f.run(t, f.acme, store.Trigger{
		ID: "run-tag", Pipeline: "deploy", TriggerSource: "github", GitSHA: "0123456789abcdef0123456789abcdef01234567",
		GithubOwner: "acme", GithubRepo: "api",
		TriggerEnv: map[string]string{sparkwing.EnvGitHubEventName: "push", "GITHUB_REF": "refs/tags/v1.2.3", "GITHUB_REF_TYPE": "tag", "GITHUB_TAG": "v1.2.3"},
	})
	raw := f.runner(t, f.acme, "agent:acme")
	srv := serveOIDC(t, f.st, oidcKeyPEM(t), nil)
	c := client.NewWithToken(srv.URL, nil, raw)
	if _, err := c.ClaimSpecificTrigger(ctx, "run-tag", time.Minute); err != nil {
		t.Fatal(err)
	}
	tok, err := c.OIDCToken(ctx, "run-tag", "sts.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyAgainst(t, srv, tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "team:acme:pipeline:deploy:trigger:push:runner:runner:ref:refs/tags/v1.2.3" || claims.Ref != "refs/tags/v1.2.3" {
		t.Fatalf("tag sub/ref = %q/%q", claims.Sub, claims.Ref)
	}
}

package cache

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func testGrantKey(token string) string { return token + "-grant-key" }

var (
	testClaim = &authwire.CacheClaim{
		Kind: authwire.CacheClaimToken, NodeID: "build", Generation: 1,
		Principal: "agent:runner", TokenPrefix: "swc_test",
	}
	testScope = &authwire.CacheScope{Repo: "github.com/acme/app", Refs: []string{"refs/heads/main"}}
)

func mintGrant(t *testing.T, key, team, run string, now time.Time, scope *authwire.CacheScope) string {
	t.Helper()
	g, err := authwire.MintClaimCacheGrant(key, team, run, now, time.Hour, testClaim, scope)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func grantFor(t *testing.T, token, team string) string {
	t.Helper()
	return mintGrant(t, testGrantKey(token), team, "run-"+team, time.Now(), testScope)
}

// safety: the derivation is the wire contract authwire pins, repeated here because a controller
// that predates claims and scopes signed grants authwire no longer mints.
func signWholeTeamGrant(key, team string, expires time.Time) string {
	payload := fmt.Sprintf(`{"t":%q,"r":"run-1","e":%d}`, team, expires.Unix())
	body := authwire.CacheGrantPrefix + base64.RawURLEncoding.EncodeToString([]byte(payload))
	derived := sha256.Sum256([]byte("sparkwing cache grant v1\x00" + key))
	mac := hmac.New(sha256.New, derived[:])
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func send(t *testing.T, srv *httptest.Server, method, path, bearer, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// Two teams' runners hold grants for the same cache. Every key one team writes
// must be invisible to, and unwritable by, the other, even when both name the
// same key.
func TestGrantsKeepEachTeamsBlobsApart(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	teamA, teamB := grantFor(t, token, "team-a"), grantFor(t, token, "team-b")

	writes := []struct{ method, write, read string }{
		{http.MethodPut, "/bin/deadbeef", "/bin/deadbeef"},
		{http.MethodPut, "/cache/go-mod-abc", "/cache/go-mod-abc"},
	}
	for _, w := range writes {
		if code, body := send(t, srv, w.method, w.write, teamA, "team-a secret"); code/100 != 2 {
			t.Fatalf("%s %s as team A = %d: %s", w.method, w.write, code, body)
		}
		if code, body := send(t, srv, http.MethodGet, w.read, teamA, ""); code != http.StatusOK || body != "team-a secret" {
			t.Errorf("team A reading its own %s = %d %q", w.read, code, body)
		}
		if code, body := send(t, srv, http.MethodGet, w.read, teamB, ""); code == http.StatusOK || strings.Contains(body, "team-a secret") {
			t.Errorf("team B read team A's %s: %d %q", w.read, code, body)
		}
		if code, body := send(t, srv, http.MethodGet, w.read, token, ""); strings.Contains(body, "team-a secret") {
			t.Errorf("the operator's unscoped tree served team A's %s: %d", w.read, code)
		}
		if code, body := send(t, srv, w.method, w.write, teamB, "team-b poison"); code/100 != 2 {
			t.Fatalf("%s %s as team B = %d: %s", w.method, w.write, code, body)
		}
		if _, body := send(t, srv, http.MethodGet, w.read, teamA, ""); body != "team-a secret" {
			t.Errorf("team B's write replaced team A's %s with %q", w.read, body)
		}
	}
}

// A grant opens the blob stores and cloning. The admin routes act on the
// whole store.
func TestGrantsDoNotReachMirrorsOrAdminRoutes(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	grant := grantFor(t, token, "team-a")

	operatorOnly := []struct{ method, path string }{
		{http.MethodPost, "/admin/store-ceiling/thaw"},
		{http.MethodPost, "/admin/store-ceiling/measure"},
		{http.MethodDelete, "/admin/teams/team-a"},
	}
	for _, r := range operatorOnly {
		if code, body := send(t, srv, r.method, r.path, grant, ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s with a grant = %d, want 401: %s", r.method, r.path, code, body)
		}
	}
}

// The source-read, upload, seed, refresh and job-artifact routes are gone,
// so even the operator token reaches nothing there.
func TestRemovedRoutesAnswerNotFound(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})

	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/archive?repo=https://github.com/acme/app.git&branch=main"},
		{http.MethodGet, "/file?repo=https://github.com/acme/app.git&branch=main&path=README"},
		{http.MethodGet, "/tree-hash?repo=https://github.com/acme/app.git&branch=main"},
		{http.MethodGet, "/branch-contains?repo=https://github.com/acme/app.git&branch=main&commit=main"},
		{http.MethodPost, "/upload"},
		{http.MethodGet, "/uploads/abc"},
		{http.MethodPost, "/sync/negotiate"},
		{http.MethodPost, "/sync/seed?repo=https://github.com/acme/app.git&sha=" + strings.Repeat("a", 40)},
		{http.MethodPost, "/git/refresh?repo=https://github.com/acme/app.git"},
		{http.MethodPost, "/artifacts/job-1?path=out.txt"},
		{http.MethodGet, "/artifacts/job-1?glob=*"},
	} {
		// safety: /git/refresh now lands on the clone route, which reads "refresh" as a repository name.
		want := http.StatusNotFound
		if strings.HasPrefix(r.path, "/git/") {
			want = http.StatusBadRequest
		}
		if code, body := send(t, srv, r.method, r.path, token, "x"); code != want {
			t.Errorf("%s %s with the operator token = %d, want %d: %s", r.method, r.path, code, want, body)
		}
	}
}

func TestCacheRefusesGrantsItCannotVerify(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	forged := grantFor(t, "some-other-token", "team-a")
	expired := mintGrant(t, testGrantKey(token), "team-a", "run-1", time.Now().Add(-2*time.Hour), testScope)
	wholeTeam := signWholeTeamGrant(testGrantKey(token), "team-a", time.Now().Add(time.Hour))
	for name, bearer := range map[string]string{"forged": forged, "expired": expired, "whole-team": wholeTeam, "none": ""} {
		if code, _ := send(t, srv, http.MethodPut, "/bin/deadbeef", bearer, "x"); code != http.StatusUnauthorized {
			t.Errorf("%s grant PUT /bin = %d, want 401", name, code)
		}
	}
}

// A team's bins count against the store ceiling, so a runner cannot fill the
// volume through them.
func TestTeamBinWritesStopAtTheStoreCeiling(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	previous := storeCeiling
	storeCeiling = objectguard.NewCeiling(objectguard.CeilingConfig{
		Limit:   objectguard.CeilingLimit{MaxBytes: 16},
		Subject: storeCeilingSubject, Remedy: storeCeilingRemedy,
	})
	t.Cleanup(func() { storeCeiling = previous })
	storeCeiling.Observe(objectguard.Usage{Bytes: 4096, Objects: 3})

	if code, body := send(t, srv, http.MethodPut, "/bin/deadbeef", grantFor(t, token, "team-a"), "x"); code != http.StatusInsufficientStorage {
		t.Errorf("team bin PUT over the ceiling = %d, want 507: %s", code, body)
	}
}

// The mirrors are shared, so another team's grant never reads a mirror the
// operator registered from a private origin, while a grant for the operator's
// own run reads it: that is how the operator's runners clone its private
// repositories. The mirror is registered before the test starts, as one the
// operator token registered before grants existed would be.
func TestOnlyOperatorGrantsReadPrivateMirrors(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	grant := grantFor(t, token, "team-a")
	operatorGrant := grantFor(t, token, authwire.OperatorTeam)
	private := "ssh://git@github.com/acme/private.git"

	repoNamesMu.Lock()
	saved := repoNames
	repoNames = map[string]string{"operator-private": private}
	repoNamesMu.Unlock()
	t.Cleanup(func() {
		repoNamesMu.Lock()
		repoNames = saved
		repoNamesMu.Unlock()
	})
	if out, err := exec.Command("git", "init", "--bare", filepath.Join(repoDir, repoHash(private)+".git")).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if code, _ := send(t, srv, http.MethodGet, "/git/operator-private/info/refs?service=git-upload-pack", token, ""); code != http.StatusOK {
		t.Fatalf("operator reading its own mirror = %d, want 200", code)
	}
	if code, body := send(t, srv, http.MethodGet, "/git/operator-private/info/refs?service=git-upload-pack", operatorGrant, ""); code != http.StatusOK {
		t.Errorf("the operator's own grant reading its private mirror = %d, want 200: %s", code, body)
	}
	if code, body := send(t, srv, http.MethodGet, "/git/operator-private/info/refs?service=git-upload-pack", grant, ""); code != http.StatusNotFound {
		t.Errorf("another team's grant reading the operator's private mirror = %d, want 404: %s", code, body)
	}
}

// A grant for the operator's own run registers the mirror its first fetch
// needs, private origin included, as the operator token did for its runners.
// Another team's grant still cannot, so it never reaches the cache's own
// credentials.
func TestOperatorGrantsRegisterPrivateMirrors(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	private := "ssh://git@git.example.invalid/acme/private.git"
	name := sourceurl.ClaimedRepoNameFromURL(private)
	path := "/git/register?name=" + url.QueryEscape(name) + "&repo=" + url.QueryEscape(private)
	t.Cleanup(func() {
		repoNamesMu.Lock()
		delete(repoNames, name)
		repoNamesMu.Unlock()
	})

	if code, body := send(t, srv, http.MethodPost, path, grantFor(t, token, "team-a"), ""); code != http.StatusForbidden {
		t.Errorf("another team's grant registering a private mirror = %d, want 403: %s", code, body)
	}
	if code, body := send(t, srv, http.MethodPost, path, grantFor(t, token, authwire.OperatorTeam), ""); code != http.StatusOK {
		t.Fatalf("the operator's own grant registering a private mirror = %d, want 200: %s", code, body)
	}
	repoNamesMu.RLock()
	got := repoNames[name]
	repoNamesMu.RUnlock()
	if got != private {
		t.Errorf("registered %q as %q, want %q", name, got, private)
	}
}

// Registering a mirror clones a repository onto the cache volume, so it stays
// with the operator: a grant cannot add mirrors, even of a public origin under
// its derived name.
func TestGrantsCannotRegisterMirrors(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	grant := grantFor(t, token, "team-a")
	public := "https://git.example.invalid/acme/public.git"
	name := sourceurl.ClaimedRepoNameFromURL(public)
	path := "/git/register?name=" + url.QueryEscape(name) + "&repo=" + url.QueryEscape(public)
	if code, body := send(t, srv, http.MethodPost, path, grant, ""); code != http.StatusForbidden {
		t.Errorf("grant registering a public mirror under its derived name = %d, want 403: %s", code, body)
	}
	repoNamesMu.RLock()
	_, registered := repoNames[name]
	repoNamesMu.RUnlock()
	if registered {
		t.Errorf("a refused grant registration left %q registered", name)
	}
	if code, body := send(t, srv, http.MethodPost, path, token, ""); code != http.StatusOK {
		t.Errorf("operator registering the same mirror = %d, want 200: %s", code, body)
	}
}

// The mirrors sit on the same volume as the blob stores, so the store ceiling
// counts them.
func TestStoreCeilingCountsTheMirrors(t *testing.T) {
	newBudgetedServer(t, "operator-token", egress.Config{})
	previous := storeCeiling
	storeCeiling = objectguard.NewCeiling(objectguard.CeilingConfig{
		Limit:   objectguard.CeilingLimit{MaxBytes: 1 << 40},
		Subject: storeCeilingSubject, Remedy: storeCeilingRemedy,
	})
	t.Cleanup(func() { storeCeiling = previous })
	mirror := filepath.Join(repoDir, "0123.git", "objects", "pack")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mirror, "pack-1.pack"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	measureStore(t.Context())
	if got := storeCeiling.State().Bytes; got < 4096 {
		t.Fatalf("store ceiling measured %d bytes, want the 4096-byte mirror pack counted", got)
	}
}

// The cache verifies grants with its grant key and nothing else, so its
// operator token, which signs nothing, cannot stand in for the key.
func TestCacheVerifiesGrantsWithTheGrantKeyAlone(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	byToken := mintGrant(t, token, "team-a", "run-1", time.Now(), testScope)
	if code, _ := send(t, srv, http.MethodPut, "/bin/deadbeef", byToken, "x"); code != http.StatusUnauthorized {
		t.Errorf("a grant signed with the operator token = %d, want 401", code)
	}
	if code, body := send(t, srv, http.MethodPut, "/bin/deadbeef", grantFor(t, token, "team-a"), "x"); code/100 != 2 {
		t.Errorf("a grant signed with the grant key = %d, want 2xx: %s", code, body)
	}
}

func TestCacheRefusesAGrantKeyThatIsItsToken(t *testing.T) {
	newBudgetedServer(t, "operator-token", egress.Config{})
	c := DefaultConfig()
	c.DataDir = t.TempDir()
	c.APIToken = "same-secret"
	c.GrantKey = "same-secret"
	if _, err := New(c); err == nil {
		t.Fatal("New accepted a grant key equal to the operator token")
	}
}

// Deleting a team removes every blob its grants wrote and nothing another
// team wrote, and only the operator token may ask.
func TestDeletingATeamTreeRemovesOnlyThatTeamsBlobs(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	teamA, teamB := grantFor(t, token, "team-a"), grantFor(t, token, "team-b")
	for _, g := range []string{teamA, teamB} {
		if code, body := send(t, srv, http.MethodPut, "/cache/key-1", g, "bytes"); code/100 != 2 {
			t.Fatalf("seed = %d: %s", code, body)
		}
	}

	if code, _ := send(t, srv, http.MethodDelete, "/admin/teams/team-a", teamA, ""); code != http.StatusUnauthorized {
		t.Fatalf("a team grant deleting a tree = %d, want 401", code)
	}
	for _, bad := range []string{"/admin/teams/team-a/artifacts", "/admin/teams/%2E%2E", "/admin/teams/"} {
		if code, _ := send(t, srv, http.MethodDelete, bad, token, ""); code != http.StatusBadRequest {
			t.Fatalf("DELETE %s = %d, want 400", bad, code)
		}
	}
	if code, body := send(t, srv, http.MethodDelete, "/admin/teams/team-a", token, ""); code != http.StatusNoContent {
		t.Fatalf("operator delete = %d: %s", code, body)
	}
	if code, _ := send(t, srv, http.MethodGet, "/cache/key-1", teamA, ""); code == http.StatusOK {
		t.Fatal("team A's blob survived its tree's deletion")
	}
	if code, body := send(t, srv, http.MethodGet, "/cache/key-1", teamB, ""); code != http.StatusOK || body != "bytes" {
		t.Fatalf("team B's blob after deleting team A = %d %q", code, body)
	}
	if code, _ := send(t, srv, http.MethodDelete, "/admin/teams/team-a", token, ""); code != http.StatusNoContent {
		t.Fatalf("repeating the delete = %d, want 204", code)
	}
}

// Another team's grant reads a shared mirror only when the mirror could hold
// nothing private: a credential-free https origin under the name that origin
// derives.
func TestGrantsReadOnlyPublicMirrorsUnderTheirDerivedName(t *testing.T) {
	const public = "https://github.com/acme/widgets.git"
	name := sourceurl.ClaimedRepoNameFromURL(public)
	cases := []struct {
		name, url string
		want      bool
	}{
		{name, public, true},
		{"widgets", public, false},
		{sourceurl.ClaimedRepoNameFromURL("http://github.com/acme/widgets.git"), "http://github.com/acme/widgets.git", false},
		{sourceurl.ClaimedRepoNameFromURL("https://x:tok@github.com/acme/widgets.git"), "https://x:tok@github.com/acme/widgets.git", false},
	}
	for _, c := range cases {
		if got := grantMayUseMirror(c.name, c.url); got != c.want {
			t.Errorf("grantMayUseMirror(%q, %q) = %v, want %v", c.name, c.url, got, c.want)
		}
	}

	repoNamesMu.Lock()
	saved := repoNames
	repoNames = map[string]string{name: public}
	repoNamesMu.Unlock()
	t.Cleanup(func() {
		repoNamesMu.Lock()
		repoNames = saved
		repoNamesMu.Unlock()
	})
	grant := cacheCaller{team: "team-a", run: "run-1"}
	if !grant.mayReadMirror(name) {
		t.Error("a team grant cannot read a registered public mirror")
	}
	if grant.mayReadMirror("repo-unregistered") {
		t.Error("a team grant may read a mirror no URL is registered for")
	}
}

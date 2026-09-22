package cache

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func grantFor(t *testing.T, token, team string) string {
	t.Helper()
	g, err := authwire.MintCacheGrant(token, team, "run-"+team, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return g
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
		{http.MethodPost, "/artifacts/run-1?path=out.txt", "/artifacts/run-1?glob=out.txt"},
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
		// Team B writing the same key lands in its own tree and leaves A's bytes alone.
		if code, body := send(t, srv, w.method, w.write, teamB, "team-b poison"); code/100 != 2 {
			t.Fatalf("%s %s as team B = %d: %s", w.method, w.write, code, body)
		}
		if _, body := send(t, srv, http.MethodGet, w.read, teamA, ""); body != "team-a secret" {
			t.Errorf("team B's write replaced team A's %s with %q", w.read, body)
		}
	}
}

// A grant opens the blob stores and cloning. Seeding, archives, uploads and the
// admin routes act on the shared mirrors or the whole store.
func TestGrantsDoNotReachMirrorsOrAdminRoutes(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	grant := grantFor(t, token, "team-a")

	operatorOnly := []struct{ method, path string }{
		{http.MethodGet, "/repos"},
		{http.MethodGet, "/archive?repo=https://github.com/acme/private.git&ref=main"},
		{http.MethodGet, "/file?repo=https://github.com/acme/private.git&ref=main&path=README"},
		{http.MethodPost, "/git/refresh?repo=https://github.com/acme/private.git"},
		{http.MethodPost, "/sync/seed?repo=https://github.com/acme/private.git&sha=" + strings.Repeat("a", 40)},
		{http.MethodPost, "/sync/negotiate"},
		{http.MethodPost, "/upload"},
		{http.MethodGet, "/uploads/abc"},
		{http.MethodPost, "/admin/store-ceiling/thaw"},
	}
	for _, r := range operatorOnly {
		if code, body := send(t, srv, r.method, r.path, grant, ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s with a grant = %d, want 401: %s", r.method, r.path, code, body)
		}
	}
}

func TestCacheRefusesGrantsItCannotVerify(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	forged := grantFor(t, "some-other-token", "team-a")
	expired, err := authwire.MintCacheGrant(token, "team-a", "run-1", time.Now().Add(-2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, bearer := range map[string]string{"forged": forged, "expired": expired, "none": ""} {
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

// The mirrors are shared, so a grant clones only a public https origin, under
// the name derived from its URL, and never a mirror the operator registered
// from a private origin.
func TestGrantsCloneOnlyPublicMirrorsUnderTheirDerivedName(t *testing.T) {
	const token = "operator-token"
	srv := newBudgetedServer(t, token, egress.Config{})
	grant := grantFor(t, token, "team-a")

	private := "ssh://git@github.com/acme/private.git"
	public := "https://github.com/acme/public.git"
	refused := []struct{ name, repo string }{
		{sourceurl.ClaimedRepoNameFromURL(private), private},
		{"public", public},
	}
	for _, c := range refused {
		path := "/git/register?name=" + url.QueryEscape(c.name) + "&repo=" + url.QueryEscape(c.repo)
		if code, body := send(t, srv, http.MethodPost, path, grant, ""); code != http.StatusForbidden {
			t.Errorf("grant registering %s as %q = %d, want 403: %s", c.repo, c.name, code, body)
		}
	}

	repoNamesMu.Lock()
	saved := repoNames
	repoNames = map[string]string{"operator-private": private}
	repoNamesMu.Unlock()
	t.Cleanup(func() {
		repoNamesMu.Lock()
		repoNames = saved
		repoNamesMu.Unlock()
	})
	// The mirror exists, so only the grant check stands between the grant and it.
	if out, err := exec.Command("git", "init", "--bare", filepath.Join(repoDir, repoHash(private)+".git")).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if code, _ := send(t, srv, http.MethodGet, "/git/operator-private/info/refs?service=git-upload-pack", token, ""); code != http.StatusOK {
		t.Fatalf("operator reading its own mirror = %d, want 200", code)
	}
	if code, body := send(t, srv, http.MethodGet, "/git/operator-private/info/refs?service=git-upload-pack", grant, ""); code != http.StatusNotFound {
		t.Errorf("grant reading the operator's private mirror = %d, want 404: %s", code, body)
	}
}

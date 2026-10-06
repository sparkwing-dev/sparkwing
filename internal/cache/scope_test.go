package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

func scopedGrant(t *testing.T, token, repo string, refs ...string) string {
	t.Helper()
	g, err := authwire.MintClaimCacheGrant(testGrantKey(token), "team-a", "run-"+refs[0], time.Now(), time.Hour,
		nil, &authwire.CacheScope{Repo: repo, Refs: refs})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// A pull request's run reads what its base branch wrote but cannot replace or
// delete it, another repository of the team reads none of it, and an entry
// written before grants carried a scope stays readable without being
// overwritten. The volume and the bucket keep the same boundary.
func TestGrantsScopeBlobsToTheRunsRepositoryAndRef(t *testing.T) {
	const token = "operator-token"
	servers := map[string]func(t *testing.T) *httptest.Server{
		"volume": func(t *testing.T) *httptest.Server { return newBudgetedServer(t, token, egress.Config{}) },
		"bucket": func(t *testing.T) *httptest.Server { srv, _ := newBlobServer(t, token); return srv },
	}
	for name, open := range servers {
		t.Run(name, func(t *testing.T) {
			srv := open(t)
			main := scopedGrant(t, token, "github:1", "refs/heads/main")
			feature := scopedGrant(t, token, "github:1", "refs/heads/feature", "refs/heads/main")
			otherRepo := scopedGrant(t, token, "github:2", "refs/heads/main")
			legacy := grantFor(t, token, "team-a")

			entries := []struct{ method, write, read string }{
				{http.MethodPut, "/bin/01234567-89abcdef", "/bin/01234567-89abcdef"},
				{http.MethodPut, "/cache/dep-go-linux-amd64-abc", "/cache/dep-go-linux-amd64-abc"},
				{http.MethodPost, "/artifacts/job-1?path=out.txt", "/artifacts/job-1?glob=out.txt"},
			}
			for _, e := range entries {
				if code, body := send(t, srv, e.method, e.write, main, "main bytes"); code/100 != 2 {
					t.Fatalf("%s %s as main = %d: %s", e.method, e.write, code, body)
				}
				if code, body := send(t, srv, http.MethodGet, e.read, feature, ""); code != http.StatusOK || body != "main bytes" {
					t.Errorf("feature falling back to main's %s = %d %q", e.read, code, body)
				}
				if code, body := send(t, srv, http.MethodGet, e.read, otherRepo, ""); code == http.StatusOK {
					t.Errorf("another repository read main's %s: %q", e.read, body)
				}
				if code, body := send(t, srv, e.method, e.write, feature, "poison"); code/100 != 2 {
					t.Fatalf("%s %s as feature = %d: %s", e.method, e.write, code, body)
				}
				if _, body := send(t, srv, http.MethodGet, e.read, main, ""); body != "main bytes" {
					t.Errorf("feature's write replaced main's %s with %q", e.read, body)
				}
				if _, body := send(t, srv, http.MethodGet, e.read, feature, ""); body != "poison" {
					t.Errorf("feature reading its own %s = %q", e.read, body)
				}
				if _, body := send(t, srv, http.MethodGet, e.read, legacy, ""); body == "main bytes" || body == "poison" {
					t.Errorf("a scoped write reached the unscoped %s", e.read)
				}
			}
			if code, _ := send(t, srv, http.MethodDelete, "/bin/01234567-89abcdef", feature, ""); code != http.StatusNoContent {
				t.Fatalf("feature deleting its own binary = %d", code)
			}
			if code, body := send(t, srv, http.MethodGet, "/bin/01234567-89abcdef", main, ""); code != http.StatusOK || body != "main bytes" {
				t.Errorf("feature's delete removed main's binary: %d %q", code, body)
			}

			if code, body := send(t, srv, http.MethodPut, "/cache/before-scopes", legacy, "legacy bytes"); code/100 != 2 {
				t.Fatalf("unscoped write = %d: %s", code, body)
			}
			if code, body := send(t, srv, http.MethodGet, "/cache/before-scopes", otherRepo, ""); code != http.StatusOK || body != "legacy bytes" {
				t.Errorf("a scoped grant reading an unscoped entry = %d %q", code, body)
			}
			if code, body := send(t, srv, http.MethodPut, "/cache/before-scopes", feature, "poison"); code/100 != 2 {
				t.Fatalf("feature write over an unscoped key = %d: %s", code, body)
			}
			if _, body := send(t, srv, http.MethodGet, "/cache/before-scopes", main, ""); body != "legacy bytes" {
				t.Errorf("feature's write replaced the unscoped entry main reads: %q", body)
			}
		})
	}
}

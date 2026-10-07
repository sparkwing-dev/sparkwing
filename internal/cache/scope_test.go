package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

func scopedGrant(t *testing.T, token, repo string, refs ...string) string {
	t.Helper()
	return mintGrant(t, testGrantKey(token), "team-a", "run-"+refs[0], time.Now(), &authwire.CacheScope{Repo: repo, Refs: refs})
}

// A pull request's run reads what its base branch wrote but cannot replace or
// delete it, another repository of the team reads none of it, and an entry
// written before grants carried a scope stays readable without being
// overwritten. The volume and the bucket keep the same boundary.
func TestGrantsScopeBlobsToTheRunsRepositoryAndRef(t *testing.T) {
	const token = "operator-token"
	// safety: no grant writes the unscoped tree any longer, so an entry from before scopes is planted where one lay.
	servers := map[string]func(t *testing.T) (*httptest.Server, func(rel, body string)){
		"volume": func(t *testing.T) (*httptest.Server, func(rel, body string)) {
			srv := newBudgetedServer(t, token, egress.Config{})
			return srv, func(rel, body string) {
				path := filepath.Join(teamsDir, "team-a", filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		},
		"bucket": func(t *testing.T) (*httptest.Server, func(rel, body string)) {
			srv, raw := newBlobServer(t, token)
			return srv, func(rel, body string) {
				if _, err := raw.PutObject(t.Context(), &s3.PutObjectInput{
					Bucket: aws.String(blobTestBucket), Key: aws.String("cache/teams/team-a/" + rel),
					Body: strings.NewReader(body),
				}); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	for name, open := range servers {
		t.Run(name, func(t *testing.T) {
			srv, plantUnscoped := open(t)
			main := scopedGrant(t, token, "github:1", "refs/heads/main")
			feature := scopedGrant(t, token, "github:1", "refs/heads/feature", "refs/heads/main")
			otherRepo := scopedGrant(t, token, "github:2", "refs/heads/main")
			unscopedReader := grantFor(t, token, "team-a")

			entries := []struct{ method, write, read string }{
				{http.MethodPut, "/bin/01234567-89abcdef", "/bin/01234567-89abcdef"},
				{http.MethodPut, "/cache/dep-go-linux-amd64-abc", "/cache/dep-go-linux-amd64-abc"},
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
				if _, body := send(t, srv, http.MethodGet, e.read, unscopedReader, ""); body == "main bytes" || body == "poison" {
					t.Errorf("a scoped write reached the unscoped %s", e.read)
				}
			}
			if code, _ := send(t, srv, http.MethodDelete, "/bin/01234567-89abcdef", feature, ""); code != http.StatusNoContent {
				t.Fatalf("feature deleting its own binary = %d", code)
			}
			if code, body := send(t, srv, http.MethodGet, "/bin/01234567-89abcdef", main, ""); code != http.StatusOK || body != "main bytes" {
				t.Errorf("feature's delete removed main's binary: %d %q", code, body)
			}

			plantUnscoped("cache/before-scopes.tar.gz", "legacy bytes")
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

// An artifact key names its own content and every reader checks it, so a
// memoized or retried node on another ref reads what a feature ref stored.
// The cache keeps that shared copy honest: it refuses bytes that are not the
// named content and refuses a grant's delete.
func TestContentAddressedArtifactsAreSharedAcrossRefs(t *testing.T) {
	const token = "operator-token"
	servers := map[string]func(t *testing.T) *httptest.Server{
		"volume": func(t *testing.T) *httptest.Server { return newBudgetedServer(t, token, egress.Config{}) },
		"bucket": func(t *testing.T) *httptest.Server { srv, _ := newBlobServer(t, token); return srv },
	}
	sum := sha256.Sum256([]byte("manifest"))
	key := "/bin/artifacts/manifests/" + hex.EncodeToString(sum[:])
	for name, open := range servers {
		t.Run(name, func(t *testing.T) {
			srv := open(t)
			feature := scopedGrant(t, token, "github.com/acme/app", "refs/heads/feature", "refs/heads/main")
			main := scopedGrant(t, token, "github.com/acme/app", "refs/heads/main")
			if code, body := send(t, srv, http.MethodPut, key, feature, "manifest"); code != http.StatusCreated {
				t.Fatalf("feature PUT = %d: %s", code, body)
			}
			if code, body := send(t, srv, http.MethodGet, key, main, ""); code != http.StatusOK || body != "manifest" {
				t.Fatalf("main reading the feature ref's artifact = %d %q", code, body)
			}
			if code, body := send(t, srv, http.MethodPut, key, feature, "forged"); code != http.StatusBadRequest {
				t.Fatalf("PUT of bytes the key does not name = %d: %s", code, body)
			}
			if code, _ := send(t, srv, http.MethodDelete, key, feature, ""); code != http.StatusForbidden {
				t.Fatalf("a grant deleting a shared artifact = %d, want 403", code)
			}
			if code, body := send(t, srv, http.MethodGet, key, main, ""); code != http.StatusOK || body != "manifest" {
				t.Fatalf("artifact after a refused forge and delete = %d %q", code, body)
			}
		})
	}
}

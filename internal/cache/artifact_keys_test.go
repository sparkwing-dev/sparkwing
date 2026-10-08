package cache

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwingcache"
)

func TestArtifactKeysThroughCacheAdapter(t *testing.T) {
	const token = "operator-token"
	for _, backend := range []string{"filesystem", "s3"} {
		t.Run(backend, func(t *testing.T) {
			var srv *httptest.Server
			if backend == "s3" {
				srv, _ = newBlobServer(t, token)
			} else {
				srv = newBudgetedServer(t, token, egress.Config{})
			}
			teamA, teamB := grantFor(t, token, "team-a"), grantFor(t, token, "team-b")
			store := sparkwingcache.New(srv.URL, teamA, srv.Client())
			digest := strings.Repeat("a", 64)
			blobBody, manifestBody := "blob bytes", "manifest bytes"
			blobKey, manifestKey := "artifacts/blobs/"+sha256Hex(blobBody), "artifacts/manifests/"+sha256Hex(manifestBody)
			bodies := map[string]string{blobKey: blobBody, manifestKey: manifestBody, "deadbeef-cafebabe": "deadbeef-cafebabe", "deadbeef.sha256": "deadbeef.sha256"}
			for _, key := range []string{blobKey, manifestKey, "deadbeef-cafebabe", "deadbeef.sha256"} {
				content := bodies[key]
				if err := store.Put(t.Context(), key, strings.NewReader(content)); err != nil {
					t.Fatalf("Put %s: %v", key, err)
				}
				rc, err := store.Get(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(rc)
				rc.Close()
				if err != nil || string(got) != content {
					t.Fatalf("Get %s = %q, %v", key, got, err)
				}
				if found, err := store.Has(t.Context(), key); err != nil || !found {
					t.Fatalf("Has %s = %v, %v", key, found, err)
				}
				for _, bearer := range []string{"", "invalid", teamB} {
					code, body := send(t, srv, http.MethodGet, "/bin/"+key, bearer, "")
					want := http.StatusUnauthorized
					if bearer == teamB {
						want = http.StatusNotFound
					}
					if code != want || strings.Contains(body, content) {
						t.Fatalf("other caller read %s: %d %q", key, code, body)
					}
				}
				wantPut := http.StatusCreated
				if strings.HasPrefix(key, "artifacts/") {
					wantPut = http.StatusBadRequest
				}
				if code, body := send(t, srv, http.MethodPut, "/bin/"+key, teamB, "other team"); code != wantPut {
					t.Fatalf("team B Put: %d %s", code, body)
				}
				if _, body := send(t, srv, http.MethodGet, "/bin/"+key, teamA, ""); body != content {
					t.Fatalf("team B replaced %s: %q", key, body)
				}
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/bin/"+key, nil)
				req.Header.Set("Authorization", "Bearer "+teamA)
				resp, err := srv.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				sum := sha256.Sum256([]byte(content))
				if want := "sha-256=" + base64.StdEncoding.EncodeToString(sum[:]); resp.Header.Get("Digest") != want {
					t.Fatalf("digest %q, want %q", resp.Header.Get("Digest"), want)
				}
			}
			for _, key := range []string{"artifacts/blobs/" + digest[:63], "artifacts/blobs/" + strings.ToUpper(digest), "artifacts/blobs/" + digest + ".sha256", "artifacts/other/" + digest, "artifacts-blobs-" + digest, "artifacts/blobs/%2e%2e%2f" + digest, "artifacts/blobs/" + digest + "/extra"} {
				if err := store.Put(t.Context(), key, strings.NewReader("bad")); err == nil {
					t.Errorf("accepted invalid key %q", key)
				}
			}
			for _, key := range []string{blobKey, manifestKey} {
				if code, body := send(t, srv, http.MethodGet, "/bin/"+key, teamA, ""); code != http.StatusOK || body != bodies[key] {
					t.Fatalf("namespace collision: %s = %d %q", key, code, body)
				}
				if err := store.Delete(t.Context(), key); err == nil {
					t.Fatalf("a grant deleted the shared artifact %s", key)
				}
				if found, err := store.Has(t.Context(), key); err != nil || !found {
					t.Fatalf("Has after a refused delete = %v %v", found, err)
				}
			}
		})
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

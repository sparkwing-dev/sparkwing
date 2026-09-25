package bincache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectSourceBundleImportsExactSnapshotWithoutOrigin(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(filepath.Join(repo, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	git("init", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "source.txt"), []byte("from bundle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".sparkwing/source.txt")
	git("-c", "user.name=Sparkwing", "-c", "user.email=workspace@sparkwing.dev", "commit", "--quiet", "-m", "sparkwing working-tree snapshot")
	sha := git("rev-parse", "HEAD")
	git("update-ref", SeedRef(sha), sha)
	bundle := filepath.Join(t.TempDir(), "source.bundle")
	git("bundle", "create", bundle, SeedRef(sha))
	contents, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	key := "sources/" + hex.EncodeToString(digest[:]) + "/" + strings.Repeat("1", 32)
	var endpoint string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/data/download":
			if r.Header.Get("Authorization") != "Bearer grant" {
				http.Error(w, "no grant", http.StatusForbidden)
				return
			}
			var request map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["kind"] != "source" || request["key"] != key {
				http.Error(w, "wrong source", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"url": endpoint + "/bundle", "sha256": hex.EncodeToString(digest[:]), "size": len(contents)})
		case "/bundle":
			_, _ = w.Write(contents)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	endpoint = srv.URL
	got, err := FetchSourceBundleDirect(t.Context(), srv.URL, "grant", "run-source", key, sha, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(got, "source.txt"))
	if err != nil || !bytes.Equal(file, []byte("from bundle\n")) {
		t.Fatalf("imported source = %q, %v", file, err)
	}
	if _, err := FetchSourceBundleDirect(t.Context(), srv.URL, "grant", "run-source", key, strings.Repeat("a", 40), "", t.TempDir()); err == nil {
		t.Fatal("wrong snapshot commit imported")
	}
}

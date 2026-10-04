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
	checkSourceBundleImport(t, "")
}

func TestDirectSourceBundleRefusesSymlinkedSparkwing(t *testing.T) {
	for _, target := range []string{"external", "relative-external", "internal"} {
		t.Run(target, func(t *testing.T) { checkSourceBundleImport(t, target) })
	}
}

func checkSourceBundleImport(t *testing.T, linkTarget string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=Sparkwing", "GIT_AUTHOR_EMAIL=workspace@sparkwing.dev",
			"GIT_COMMITTER_NAME=Sparkwing", "GIT_COMMITTER_EMAIL=workspace@sparkwing.dev",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	sourceDir := filepath.Join(repo, ".sparkwing")
	if linkTarget == "internal" {
		sourceDir = filepath.Join(repo, "pipeline-source")
	} else if linkTarget != "" {
		sourceDir = t.TempDir()
	}
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	git("init", "--quiet")
	if err := os.WriteFile(filepath.Join(sourceDir, "source.txt"), []byte("from bundle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if linkTarget != "" {
		target := sourceDir
		if linkTarget != "external" {
			var err error
			target, err = filepath.Rel(repo, sourceDir)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, filepath.Join(repo, ".sparkwing")); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "--quiet", "-m", "sparkwing working-tree snapshot")
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
	if linkTarget != "" {
		if err == nil {
			t.Fatalf("imported symlinked source directory %s", got)
		}
		if !strings.Contains(err.Error(), "no .sparkwing directory") {
			t.Fatalf("symlink import error = %v, want source directory refusal", err)
		}
		return
	}
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

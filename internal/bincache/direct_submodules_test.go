package bincache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A private submodule named by its ssh URL is fetched over https with the
// run's one released token: url.insteadOf rewrites it onto the token's host,
// and the pipe answers the helper for the submodule fetch as it did for the
// main one. A wrong token fails the submodule, so nothing else answered.
func TestDirectSubmodulesFetchWithTheOneReleasedToken(t *testing.T) {
	repos := t.TempDir()
	const tok = "ghs_submodules"
	srv, seen := authGitServer(t, repos, tok)
	hostPort := strings.TrimPrefix(srv.URL, "http://")
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL="+os.DevNull)

	lib := filepath.Join(t.TempDir(), "lib")
	gitRun(t, env, "init", "--quiet", "-b", "main", lib)
	writeTestFile(t, filepath.Join(lib, "lib.txt"), "private library\n")
	gitRun(t, env, "-C", lib, "add", ".")
	gitRun(t, env, "-C", lib, "commit", "--quiet", "-m", "lib")
	if err := os.MkdirAll(filepath.Join(repos, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, env, "clone", "--quiet", "--bare", lib, filepath.Join(repos, "acme", "lib.git"))

	app := filepath.Join(t.TempDir(), "app")
	gitRun(t, env, "init", "--quiet", "-b", "main", app)
	writeTestFile(t, filepath.Join(app, ".sparkwing", "main.go"), "package main\n")
	gitRun(t, env, "-C", app, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", lib, "vendor/lib")
	gitRun(t, env, "-C", app, "config", "-f", ".gitmodules", "submodule.vendor/lib.url", "git@"+hostPort+":acme/lib.git")
	gitRun(t, env, "-C", app, "add", ".")
	gitRun(t, env, "-C", app, "commit", "--quiet", "-m", "app")
	gitRun(t, env, "clone", "--quiet", "--bare", app, filepath.Join(repos, "app.git"))

	cred := DirectCredential{Kind: CredentialGitHubApp, Host: hostPort, Username: "x-access-token", Secret: tok}
	opts := httpOnly
	opts.cred = cred
	dest := filepath.Join(t.TempDir(), "run")
	if err := directCheckout(context.Background(), t.TempDir(), srv.URL+"/app.git", "main", "", dest, opts); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	fetchedMain := seen.Load()
	if err := directSubmodules(context.Background(), dest, srv.URL+"/", cred, httpOnly); err != nil {
		t.Fatalf("submodules: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "vendor", "lib", "lib.txt")); err != nil || string(got) != "private library\n" {
		t.Fatalf("the submodule was not checked out: %q, %v", got, err)
	}
	if seen.Load() <= fetchedMain {
		t.Fatal("the submodule fetch never presented the token")
	}

	wrong := cred
	wrong.Secret = "ghs_wrong"
	opts.cred = cred
	again := filepath.Join(t.TempDir(), "run")
	if err := directCheckout(context.Background(), t.TempDir(), srv.URL+"/app.git", "main", "", again, opts); err != nil {
		t.Fatal(err)
	}
	err := directSubmodules(context.Background(), again, srv.URL+"/", wrong, httpOnly)
	if err == nil {
		t.Fatal("a wrong token fetched the private submodule")
	}
	if strings.Contains(err.Error(), "ghs_wrong") {
		t.Fatalf("the error carries the token: %v", err)
	}
	if err := directSubmodules(context.Background(), again, srv.URL+"/", DirectCredential{}, httpOnly); err == nil {
		t.Fatal("submodules were fetched with no released credential")
	}
}

// The runner checks out submodules only when the controller's answer names
// extra repositories a team owner listed, and then with that one answer: it
// asks nothing further, so nothing in the fetched tree can widen the token.
func TestFetchRunSourceDirectChecksOutSubmodulesOnlyForTheOwnersList(t *testing.T) {
	for name, answer := range map[string]string{
		"listed":   `{"kind":"github_app","host":"github.com","token":"ghs_wide","extra_repositories":["acme/lib"]}`,
		"unlisted": `{"kind":"github_app","host":"github.com","token":"ghs_narrow"}`,
	} {
		var mu sync.Mutex
		var asks []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			asks = append(asks, r.URL.Path)
			mu.Unlock()
			if !strings.HasSuffix(r.URL.Path, "/git-credential") {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(answer))
		}))
		t.Cleanup(srv.Close)
		checkout := t.TempDir()
		writeTestFile(t, filepath.Join(checkout, ".gitmodules"), "[submodule \"lib\"]\n")
		var subCred *DirectCredential
		_, err := fetchRunSourceDirect(context.Background(), RunSource{
			ControllerURL: srv.URL, RunnerToken: "runner-tok", RunID: "run-1",
		}, nil, func(DirectCredential) (string, error) {
			return filepath.Join(checkout, ".sparkwing"), nil
		}, func(_ string, cred DirectCredential) error {
			subCred = &cred
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(asks) != 1 {
			t.Fatalf("%s: controller asks = %v, want the one credential request", name, asks)
		}
		if name == "unlisted" {
			if subCred != nil {
				t.Fatalf("unlisted: submodules were checked out with %+v", subCred)
			}
			continue
		}
		if subCred == nil || subCred.Secret != "ghs_wide" || strings.Join(subCred.ExtraRepositories, ",") != "acme/lib" {
			t.Fatalf("listed: submodules got %+v, want the one widened answer", subCred)
		}
	}
}

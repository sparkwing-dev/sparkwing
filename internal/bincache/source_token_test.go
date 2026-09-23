package bincache

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func gitRun(t *testing.T, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// authGitServer serves dir's repositories over smart http and answers 401 to
// any request without the basic credential x-access-token:tok. seen counts the
// requests that carried it.
func authGitServer(t *testing.T, dir, tok string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	backend := filepath.Join(strings.TrimSpace(gitRun(t, os.Environ(), "--exec-path")), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skipf("no git-http-backend: %v", err)
	}
	cgiHandler := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + dir, "GIT_HTTP_EXPORT_ALL=1"}}
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "x-access-token" || pass != tok {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		seen.Add(1)
		cgiHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// The token reaches git through the inherited pipe and the helper scoped to
// one URL prefix: the fetch authenticates with it, while the environment,
// another host, and a command that touches the checkout never hold it.
func TestGitHubTokenReachesOnlyTheFetch(t *testing.T) {
	home, repos := t.TempDir(), t.TempDir()
	base := []string{
		"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=!printf 'username=x-access-token\\npassword=ambient\\n'",
	}
	src := filepath.Join(repos, "src")
	gitRun(t, base, "init", "--quiet", "-b", "main", src)
	gitRun(t, base, "-C", src, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"commit", "--quiet", "--allow-empty", "-m", "one")
	gitRun(t, base, "clone", "--quiet", "--bare", src, filepath.Join(repos, "app.git"))
	const tok = "ghs_abc123"
	srv, seen := authGitServer(t, repos, tok)
	remote := srv.URL + "/app.git"

	fetch := func(env []string, pipeTok string) error {
		mirror := filepath.Join(t.TempDir(), "m.git")
		gitRun(t, base, "init", "--quiet", "--bare", mirror)
		cmd := exec.Command("git", "-C", mirror, "fetch", "--quiet", "--depth", "1", "--", remote, "refs/heads/main")
		cmd.Env = env
		if pipeTok != "" {
			cred, err := credentialPipe(pipeTok)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cred.Close() }()
			cmd.ExtraFiles = []*os.File{cred}
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
		return nil
	}

	fetchEnv := withGitHubCredential(base, srv.URL+"/")
	if err := fetch(fetchEnv, tok); err != nil {
		t.Fatalf("fetch with the source token: %v", err)
	}
	if seen.Load() == 0 {
		t.Fatal("the fetch never presented the token")
	}
	for _, item := range fetchEnv {
		if strings.Contains(item, tok) {
			t.Fatalf("the token is in the environment: %q", item)
		}
	}
	seen.Store(0)
	if err := fetch(withGitHubCredential(base, "https://github.com/"), tok); err == nil || seen.Load() != 0 {
		t.Fatalf("a helper scoped to github.com answered for another host: err=%v, authorized=%d", err, seen.Load())
	}
	if err := fetch(fetchEnv, ""); err == nil {
		t.Fatal("control: a fetch without the pipe authenticated anyway")
	}
	local := directLocalGitEnv(fetchEnv)
	cmd := exec.Command("git", "config", "--get-urlmatch", "credential.helper", remote)
	cmd.Env = local
	if out, _ := cmd.Output(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("a checkout command sees a credential helper: %q", out)
	}
}

func TestDirectGitEnvDropsTraces(t *testing.T) {
	env := directGitEnv([]string{
		"GIT_TRACE=1", "GIT_TRACE_CURL=/tmp/x", "GIT_TRACE2_EVENT=/tmp/y",
		"GIT_CURL_VERBOSE=1", "GIT_TRACE_PACKET=1", "HOME=/h",
	})
	if len(env) != 1 || env[0] != "HOME=/h" {
		t.Fatalf("env = %v, want only HOME", env)
	}
}

func TestGitHubTokenFetchRefusesAnotherHost(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	_, err := FetchPipelineSourceDirect(context.Background(), "https://gitlab.example.com/o/r.git", "main",
		strings.Repeat("a", 40), filepath.Join(t.TempDir(), "run"), DirectCredential{GitHubToken: "ghs_x"})
	if err == nil || !strings.Contains(err.Error(), "only a github.com repository") {
		t.Fatalf("err = %v, want a refusal to send the token elsewhere", err)
	}
	if got := githubTokenRemote("git@github.com:acme/widgets.git"); got != "https://github.com/acme/widgets.git" {
		t.Fatalf("ssh remote = %q, want its https form", got)
	}
}

func TestRequestSourceToken(t *testing.T) {
	answer := `{"token":"ghs_ok","expires_at":1,"repository":"acme/widgets"}`
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runs/run-1/source-token" || r.Header.Get("Authorization") != "Bearer runner-tok" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	ctx := context.Background()
	if tok, err := RequestSourceToken(ctx, srv.URL, "runner-tok", "run-1"); err != nil || tok != "ghs_ok" {
		t.Fatalf("token = %q, %v", tok, err)
	}
	status = http.StatusNotFound
	if _, err := RequestSourceToken(ctx, srv.URL, "runner-tok", "run-1"); !errors.Is(err, ErrNoSourceToken) {
		t.Fatalf("404 = %v, want ErrNoSourceToken", err)
	}
	status, answer = http.StatusOK, `{"token":"ghs_ok\nGIT_CONFIG_KEY_9=core.sshCommand"}`
	if _, err := RequestSourceToken(ctx, srv.URL, "runner-tok", "run-1"); err == nil {
		t.Fatal("a token carrying a newline was accepted")
	}
}

func TestDirectCredentialForNeedsTheOptIn(t *testing.T) {
	t.Setenv(GitHubAppSourceEnv, "")
	cred, err := DirectCredentialFor(context.Background(), "http://127.0.0.1:1", "t", "run-1", "https://github.com/acme/widgets.git")
	if err != nil || cred.GitHubToken != "" {
		t.Fatalf("without the opt-in = %+v, %v; want no request and no token", cred, err)
	}
}

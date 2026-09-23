package bincache

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitURLMatch(t *testing.T, env []string, key, url string) string {
	t.Helper()
	cmd := exec.Command("git", "config", "--get-urlmatch", key, url)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// The token reaches git only as config in the fetch's environment, scoped to
// github.com, and never reaches a command that touches the checkout.
func TestGitHubTokenReachesOnlyTheFetch(t *testing.T) {
	home := t.TempDir()
	base := []string{
		"HOME=" + home, "PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=someone",
	}
	fetchEnv := withGitHubToken(base, "ghs_abc123")
	want := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_abc123"))
	if got := gitURLMatch(t, fetchEnv, "http.extraheader", "https://github.com/acme/widgets.git"); got != want {
		t.Fatalf("extraheader for github.com = %q, want %q", got, want)
	}
	if got := gitURLMatch(t, fetchEnv, "http.extraheader", "https://evil.example.com/acme/widgets.git"); got != "" {
		t.Fatalf("the token reaches another host: %q", got)
	}
	if got := gitURLMatch(t, fetchEnv, "user.name", "https://github.com/"); got != "someone" {
		t.Fatalf("the environment's own config entry was lost: %q", got)
	}
	local := directLocalGitEnv(fetchEnv)
	if got := gitURLMatch(t, local, "http.extraheader", "https://github.com/acme/widgets.git"); got != "" {
		t.Fatalf("a checkout command sees the token: %q", got)
	}
	for _, item := range fetchEnv {
		if strings.Contains(item, "ghs_abc123") {
			t.Fatalf("the raw token is in the environment: %q", item)
		}
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

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var triggerTestGitObjectRE = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

// triggerSpy is a minimal controller stand-in. It records request lines
// and captures trigger POST bodies, and serves just enough of the
// status-follow surface (GetRun returns a terminal run, ListNodes
// returns empty) for a non-detach follow to render once and exit.
type triggerSpy struct {
	mu             sync.Mutex
	reqs           []string
	bodies         [][]byte
	failRefresh    bool
	seedBodyBytes  int
	seedRepoValues []string
	seedSHAValues  []string
}

func (s *triggerSpy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Method+" "+r.URL.Path)
		s.mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/gitcache/refresh":
			if s.failRefresh {
				http.Error(w, "refresh failed", http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/gitcache/seed":
			body, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.seedBodyBytes += len(body)
			s.seedRepoValues = append(s.seedRepoValues, r.URL.Query().Get("repo"))
			s.seedSHAValues = append(s.seedSHAValues, r.URL.Query().Get("sha"))
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers":
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			s.mu.Lock()
			s.bodies = append(s.bodies, body)
			s.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.TriggerResponse{RunID: "run-test", Status: "pending"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/nodes"):
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runs/run-test":
			_ = json.NewEncoder(w).Encode(store.Run{ID: "run-test", Pipeline: "release", Status: "success", StartedAt: time.Now()})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	})
}

func (s *triggerSpy) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reqs...)
}

func (s *triggerSpy) seedStats() (int, []string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seedBodyBytes, append([]string(nil), s.seedRepoValues...), append([]string(nil), s.seedSHAValues...)
}

func writeTriggerProfiles(t *testing.T, controllerURL string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	body := "profiles:\n" +
		"  prod: { controller: { url: " + controllerURL + " } }\n" +
		"  laptop: { state: { type: sqlite } }\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}
	t.Setenv("SPARKWING_PROFILES", path)
}

func TestPipelineTrigger_MissingProfile(t *testing.T) {
	err := runPipelineTrigger([]string{"release"})
	if err == nil {
		t.Fatal("expected --profile-required error")
	}
	if !strings.Contains(err.Error(), "--profile NAME is required") {
		t.Errorf("message = %q", err.Error())
	}
	if code := exitCodeFor(err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestPipelineTrigger_ProfileNotFound(t *testing.T) {
	writeTriggerProfiles(t, "https://api.example.dev")
	err := runPipelineTrigger([]string{"release", "--profile", "bogus"})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), `profile "bogus" not found`) {
		t.Errorf("message = %q", err.Error())
	}
}

func TestPipelineTrigger_NoController(t *testing.T) {
	writeTriggerProfiles(t, "https://api.example.dev")
	err := runPipelineTrigger([]string{"release", "--profile", "laptop"})
	if err == nil {
		t.Fatal("expected no-controller error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `profile "laptop" has no controller`) {
		t.Errorf("message should name the controller-less profile: %q", msg)
	}
	if !strings.Contains(msg, "sparkwing run --profile laptop") {
		t.Errorf("message should point at the local-run alternative: %q", msg)
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestPipelineTrigger_DetachFiresTriggerOnly(t *testing.T) {
	spy := &triggerSpy{}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfiles(t, srv.URL)

	out := captureStdout(t, func() {
		if err := runPipelineTrigger([]string{"release", "--profile", "prod", "--detach", "--version", "v1.2.3"}); err != nil {
			t.Errorf("trigger: %v", err)
		}
	})

	reqs := spy.requests()
	sawTrigger := false
	for _, r := range reqs {
		switch {
		case r == "POST /api/v1/triggers":
			sawTrigger = true
		case r == "GET /api/v1/services":
		case r == "POST /api/v1/gitcache/refresh":
		case strings.HasPrefix(r, "GET /api/v1/runs"):
			t.Fatalf("detach should not follow the run; got %v", reqs)
		}
	}
	if !sawTrigger {
		t.Fatalf("expected POST /api/v1/triggers; got %v", reqs)
	}
	if strings.TrimSpace(out) != "run-test" {
		t.Errorf("detach stdout = %q, want run id", out)
	}
	if len(spy.bodies) != 1 {
		t.Fatalf("expected 1 trigger body, got %d", len(spy.bodies))
	}
	var req client.TriggerRequest
	if err := json.Unmarshal(spy.bodies[0], &req); err != nil {
		t.Fatalf("decode trigger body: %v", err)
	}
	if req.Pipeline != "release" {
		t.Errorf("pipeline = %q, want release", req.Pipeline)
	}
	if req.Args["version"] != "v1.2.3" {
		t.Errorf("args = %v, want version=v1.2.3", req.Args)
	}
	if !strings.HasPrefix(req.Trigger.Source, "pipeline-trigger") {
		t.Errorf("trigger source = %q, want pipeline-trigger prefix", req.Trigger.Source)
	}
}

func TestPipelineTrigger_DetachAcceptsNonGitHubOrigin(t *testing.T) {
	spy := &triggerSpy{}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfiles(t, srv.URL)
	origin := "https://git.example.com/acme/widgets.git"
	withGitCheckout(t, origin, func() {
		out := captureStdout(t, func() {
			if err := runPipelineTrigger([]string{"release", "--profile", "prod", "--detach"}); err != nil {
				t.Errorf("trigger: %v", err)
			}
		})
		if strings.TrimSpace(out) != "run-test" {
			t.Errorf("detach stdout = %q, want run id", out)
		}
	})

	if len(spy.bodies) != 1 {
		t.Fatalf("expected 1 trigger body, got %d", len(spy.bodies))
	}
	var req client.TriggerRequest
	if err := json.Unmarshal(spy.bodies[0], &req); err != nil {
		t.Fatalf("decode trigger body: %v", err)
	}
	if req.Git.RepoURL != origin {
		t.Fatalf("repo_url = %q, want %q", req.Git.RepoURL, origin)
	}
	if req.Git.GithubOwner != "" || req.Git.GithubRepo != "" {
		t.Fatalf("github fields = %q/%q, want empty for non-GitHub origin", req.Git.GithubOwner, req.Git.GithubRepo)
	}
	if got := req.Trigger.Env["GITHUB_REPOSITORY"]; got != "" {
		t.Fatalf("GITHUB_REPOSITORY = %q, want empty for non-GitHub origin", got)
	}
}

func TestPipelineTrigger_SeedsControllerGitcacheWhenRefreshFails(t *testing.T) {
	spy := &triggerSpy{failRefresh: true}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfiles(t, srv.URL)
	origin := "https://git.example.com/acme/widgets.git"
	withGitCheckout(t, origin, func() {
		out := captureStdout(t, func() {
			if err := runPipelineTrigger([]string{"release", "--profile", "prod", "--detach"}); err != nil {
				t.Errorf("trigger: %v", err)
			}
		})
		if strings.TrimSpace(out) != "run-test" {
			t.Errorf("detach stdout = %q, want run id", out)
		}
	})

	size, repos, shas := spy.seedStats()
	if size == 0 {
		t.Fatal("expected non-empty git bundle seed body")
	}
	if len(repos) != 1 || repos[0] != origin {
		t.Fatalf("seed repos = %v, want [%s]", repos, origin)
	}
	if len(shas) != 1 || !triggerTestGitObjectRE.MatchString(shas[0]) {
		t.Fatalf("seed shas = %v, want one git object id", shas)
	}
	reqs := spy.requests()
	if !slices.Contains(reqs, "POST /api/v1/gitcache/refresh") {
		t.Fatalf("expected refresh before seed; got %v", reqs)
	}
	if !slices.Contains(reqs, "POST /api/v1/gitcache/seed") {
		t.Fatalf("expected seed fallback; got %v", reqs)
	}
}

func TestPipelineTrigger_DetachCanonicalizesGitHubHTTPOrigin(t *testing.T) {
	spy := &triggerSpy{}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfiles(t, srv.URL)
	withGitCheckout(t, "http://github.com/sparkwing-dev/sparkwing.git", func() {
		out := captureStdout(t, func() {
			if err := runPipelineTrigger([]string{"release", "--profile", "prod", "--detach"}); err != nil {
				t.Errorf("trigger: %v", err)
			}
		})
		if strings.TrimSpace(out) != "run-test" {
			t.Errorf("detach stdout = %q, want run id", out)
		}
	})

	if len(spy.bodies) != 1 {
		t.Fatalf("expected 1 trigger body, got %d", len(spy.bodies))
	}
	var req client.TriggerRequest
	if err := json.Unmarshal(spy.bodies[0], &req); err != nil {
		t.Fatalf("decode trigger body: %v", err)
	}
	if req.Git.RepoURL != "git@github.com:sparkwing-dev/sparkwing.git" {
		t.Fatalf("repo_url = %q, want canonical GitHub SSH URL", req.Git.RepoURL)
	}
	if got := req.Trigger.Env["GITHUB_REPOSITORY"]; got != "sparkwing-dev/sparkwing" {
		t.Fatalf("GITHUB_REPOSITORY = %q, want sparkwing-dev/sparkwing", got)
	}
	if req.Git.GithubOwner != "sparkwing-dev" || req.Git.GithubRepo != "sparkwing" {
		t.Fatalf("github fields = %q/%q, want sparkwing-dev/sparkwing", req.Git.GithubOwner, req.Git.GithubRepo)
	}
}

func TestPipelineTrigger_DefaultFollows(t *testing.T) {
	spy := &triggerSpy{}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfiles(t, srv.URL)

	_ = captureStdout(t, func() {
		if err := runPipelineTrigger([]string{"release", "--profile", "prod"}); err != nil {
			t.Errorf("trigger: %v", err)
		}
	})

	reqs := spy.requests()
	sawTrigger, sawFollow := false, false
	for _, r := range reqs {
		switch r {
		case "POST /api/v1/triggers":
			sawTrigger = true
		case "GET /api/v1/runs/run-test":
			sawFollow = true
		}
	}
	if !sawTrigger {
		t.Fatalf("expected POST /api/v1/triggers; got %v", reqs)
	}
	if !sawFollow {
		t.Fatalf("non-detach should follow the run (GET /api/v1/runs/run-test); got %v", reqs)
	}
}

func withGitCheckout(t *testing.T, origin string, fn func()) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("config", "user.email", "sparkwing@example.invalid")
	run("config", "user.name", "Sparkwing Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
	run("branch", "-M", "main")
	run("remote", "add", "origin", origin)

	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
	fn()
}

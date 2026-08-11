package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
	// runStatus is the terminal status GetRun reports (default
	// "success"); runError is the run-level error that goes with it.
	runStatus string
	runError  string
	// runStatuses, when set, is consumed one entry per GetRun with the
	// last entry repeating -- enough to stage a run that flips terminal
	// between the status follow's render and its terminality check.
	runStatuses []string
	getRunCalls int
	// runHTTPStatus, when non-zero, is the HTTP error GetRun returns
	// instead of a run (a controller mid-rolling-restart).
	runHTTPStatus int
}

// nextRunStatus returns the status this GetRun call should report.
func (s *triggerSpy) nextRunStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.getRunCalls
	s.getRunCalls++
	if len(s.runStatuses) > 0 {
		return s.runStatuses[min(i, len(s.runStatuses)-1)]
	}
	if s.runStatus == "" {
		return "success"
	}
	return s.runStatus
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
			s.mu.Lock()
			runErr, httpStatus := s.runError, s.runHTTPStatus
			s.mu.Unlock()
			if httpStatus != 0 {
				http.Error(w, "controller unavailable", httpStatus)
				return
			}
			status := s.nextRunStatus()
			now := time.Now()
			run := store.Run{ID: "run-test", Pipeline: "release", Status: status, StartedAt: now.Add(-time.Second)}
			if isTerminalRunStatus(status) {
				run.Error = runErr
				run.FinishedAt = &now
			}
			_ = json.NewEncoder(w).Encode(run)
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

// writeTriggerProfilesWithLogs adds a logs: surface so the follow takes
// the log-streaming arm (followLogsRemote) instead of the status arm.
func writeTriggerProfilesWithLogs(t *testing.T, controllerURL string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	body := "profiles:\n" +
		"  prod: { controller: { url: " + controllerURL + " }, logs: { type: controller } }\n"
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

// TestPipelineTrigger_FollowExitsOnRunOutcome is the scripted contract
// CI wraps: a non-detach trigger must exit like the local run it
// stands in for -- 0 only when the remote run succeeded, 1 when it
// failed or was cancelled. Before this, the follow returned nil no
// matter how the run ended and wrappers read a failed run as success.
func TestPipelineTrigger_FollowExitsOnRunOutcome(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		wantCode int
	}{
		{name: "success", status: "success", wantCode: 0},
		{name: "failed", status: "failed", wantCode: 1},
		{name: "cancelled", status: "cancelled", wantCode: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &triggerSpy{runStatus: tc.status, runError: "node build failed"}
			srv := httptest.NewServer(spy.handler())
			defer srv.Close()
			writeTriggerProfiles(t, srv.URL)

			var err error
			stderr := captureStderr(t, func() {
				_ = captureStdout(t, func() {
					err = runPipelineTrigger([]string{"release", "--profile", "prod"})
				})
			})

			if got := exitCodeFor(err); got != tc.wantCode {
				t.Fatalf("exit code = %d (err=%v), want %d", got, err, tc.wantCode)
			}
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("successful run should not error: %v", err)
				}
				return
			}
			if !strings.Contains(err.Error(), tc.status) || !strings.Contains(err.Error(), "run-test") {
				t.Errorf("error should name the run and its status; got %q", err.Error())
			}
			// The status arm renders to stdout as it polls, so the
			// summary has to reach stderr too or `> run.log` swallows
			// every trace of the failure.
			for _, want := range []string{"run-test", "status:    " + tc.status, "node build failed"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr summary missing %q; got:\n%s", want, stderr)
				}
			}
		})
	}
}

// TestPipelineTrigger_LogFollowReportsFailure covers the log-streaming
// arm of the follow, where the SSE stream simply ends when the run
// goes terminal: the summary has to come from a status read, printed
// to stderr so stdout stays a pure log stream.
func TestPipelineTrigger_LogFollowReportsFailure(t *testing.T) {
	spy := &triggerSpy{runStatus: "failed", runError: "node build failed"}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfilesWithLogs(t, srv.URL)

	var err error
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			err = runPipelineTrigger([]string{"release", "--profile", "prod"})
		})
	})

	if code := exitCodeFor(err); code != 1 {
		t.Fatalf("exit code = %d (err=%v), want 1", code, err)
	}
	for _, want := range []string{"run-test", "status:    failed", "node build failed"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr summary missing %q; got:\n%s", want, stderr)
		}
	}
}

// TestPipelineTrigger_StatusFollowRepaintsTerminalFrame covers the
// window where the run flips terminal between the status follow's
// render and its terminality check: the last frame on stdout still
// says "running", so the authoritative summary must be reprinted or
// the operator is left reading a stale frame next to exit 1.
func TestPipelineTrigger_StatusFollowRepaintsTerminalFrame(t *testing.T) {
	spy := &triggerSpy{runStatuses: []string{"running", "failed"}, runError: "node build failed"}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfiles(t, srv.URL)

	var err error
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			err = runPipelineTrigger([]string{"release", "--profile", "prod"})
		})
	})

	if code := exitCodeFor(err); code != 1 {
		t.Fatalf("exit code = %d (err=%v), want 1", code, err)
	}
	if !strings.Contains(stdout, "status:    running") {
		t.Fatalf("test no longer stages a stale frame; stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "status:    failed") {
		t.Errorf("terminal summary missing from stderr; got:\n%s", stderr)
	}
}

// TestPipelineTrigger_UnreachableControllerIsUnknownNotFailed pins the
// distinction a rolling controller restart depends on: losing the
// follow says nothing about the run, so it exits 3 with a pointer at
// the command that answers later -- never 1, which would report a
// possibly-succeeding run as failed.
func TestPipelineTrigger_UnreachableControllerIsUnknownNotFailed(t *testing.T) {
	spy := &triggerSpy{runHTTPStatus: http.StatusServiceUnavailable}
	srv := httptest.NewServer(spy.handler())
	defer srv.Close()
	writeTriggerProfiles(t, srv.URL)

	var err error
	_ = captureStderr(t, func() {
		_ = captureStdout(t, func() {
			err = runPipelineTrigger([]string{"release", "--profile", "prod"})
		})
	})

	if code := exitCodeFor(err); code != 3 {
		t.Fatalf("exit code = %d (err=%v), want 3", code, err)
	}
	msg := err.Error()
	for _, want := range []string{"outcome unknown", "may still be in progress", "sparkwing runs status --run run-test --profile prod"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q; got %q", want, msg)
		}
	}
}

// TestFollowExitResult_UnknownTerminalState pins the third arm: when
// the follow ends without a readable terminal status (dropped
// connection, cancelled context), the CLI reports what it knows and
// exits 3 rather than inventing success or failure. A follow that
// broke on a run that still reads terminal is not that case -- the
// outcome wins.
func TestFollowExitResult_UnknownTerminalState(t *testing.T) {
	fetchErr := followExitResult("prod", "run-test", "", errors.New("dial tcp: connection refused"), nil)
	if code := exitCodeFor(fetchErr); code != 3 {
		t.Errorf("unreadable status exit code = %d (err=%v), want 3", code, fetchErr)
	}
	for _, want := range []string{"connection refused", "may still be in progress", "--profile prod"} {
		if !strings.Contains(fetchErr.Error(), want) {
			t.Errorf("error missing %q; got %q", want, fetchErr.Error())
		}
	}

	stillRunning := followExitResult("prod", "run-test", "running", nil, errors.New("unexpected EOF"))
	if code := exitCodeFor(stillRunning); code != 3 {
		t.Errorf("non-terminal status exit code = %d (err=%v), want 3", code, stillRunning)
	}
	if !strings.Contains(stillRunning.Error(), "running") || !strings.Contains(stillRunning.Error(), "unexpected EOF") {
		t.Errorf("error should name the last status and why the follow ended; got %q", stillRunning.Error())
	}

	// A broken stream over a run that did reach a verdict reports the
	// verdict: the stream is how output arrived, not what happened.
	brokenButFailed := followExitResult("prod", "run-test", "failed", nil, errors.New("unexpected EOF"))
	if code := exitCodeFor(brokenButFailed); code != 1 {
		t.Errorf("terminal-despite-broken-follow exit code = %d (err=%v), want 1", code, brokenButFailed)
	}
	if err := followExitResult("prod", "run-test", "success", nil, nil); err != nil {
		t.Errorf("success should map to a nil error; got %v", err)
	}
}

// captureStderr mirrors captureStdout for the failure summary, which
// deliberately avoids stdout so piped log output stays clean.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()
	fn()
	w.Close()
	return string(<-done)
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

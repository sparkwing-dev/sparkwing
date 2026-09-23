//go:build e2e_direct_source

package controller_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// TestDirectSourceRunnerRunsATriggeredRun drives a real team runner, started
// with the exact command the machines page shows, through a run whose source
// it must fetch itself from a public GitHub repository.
//
//	go build -o /tmp/bin/ ./cmd/sparkwing-runner ./cmd/sparkwing
//	SPARKWING_E2E_RUNNER_DIR=/tmp/bin SPARKWING_E2E_SHA=<pushed sha> \
//	  go test -tags e2e_direct_source -run DirectSource -v ./pkg/controller/
//
// SPARKWING_E2E_REPO and SPARKWING_E2E_SHA pick the commit (default: the
// public sparkwing repository at SPARKWING_E2E_SHA, which must be pushed), and
// SPARKWING_E2E_PIPELINE the pipeline in its .sparkwing directory. The node
// executor test needs a pipeline with a node that is not inline
// (SPARKWING_E2E_NODE_PIPELINE).
func TestDirectSourceRunnerRunsATriggeredRun(t *testing.T) {
	e := newDirectSourceE2E(t)
	runID := e.trigger(envOr("SPARKWING_E2E_PIPELINE", "weather-report"))
	e.startRunner("runner", e.command)
	e.awaitSuccess(runID)
}

// TestDirectSourceNodeExecutorFetchesTheSource splits the work the way a
// cloud pool does: one runner claims the trigger and dispatches its nodes to
// the controller, and a second runner, which holds no pipeline binary, claims
// the node and must fetch and compile the run's source itself.
func TestDirectSourceNodeExecutorFetchesTheSource(t *testing.T) {
	e := newDirectSourceE2E(t)
	runID := e.trigger(envOr("SPARKWING_E2E_NODE_PIPELINE", "admission-stress-light-sequential"))
	e.startRunner("dispatcher", e.command+" --claim-nodes=false --trigger-runner warm")
	nodeOnly := strings.Replace(e.command, " --also-claim-triggers", "", 1)
	nodeOnly = strings.Replace(nodeOnly, "--holder-prefix alice-laptop", "--holder-prefix alice-pool --metrics-addr=", 1)
	e.startRunner("node-runner", nodeOnly)
	e.awaitSuccess(runID)
}

// TestCLITokenTriggerRunsOnADirectSourceRunner is the path a team member
// takes from a terminal: mint a CLI token in the dashboard, paste the setup
// command it returns, and trigger a pipeline from a checkout of a pushed
// commit. The trigger records the checkout's repository and commit, and a
// team runner fetches that commit itself and runs it. SPARKWING_E2E_RUNNER_DIR
// must also hold the sparkwing CLI.
func TestCLITokenTriggerRunsOnADirectSourceRunner(t *testing.T) {
	e := newDirectSourceE2E(t)
	var minted struct {
		Token   string `json:"token"`
		Profile string `json:"profile"`
		Setup   string `json:"setup"`
		Run     string `json:"run"`
	}
	if code := e.f.call("POST", "/api/v1/team/cli-tokens", e.alice.auth, nil, &minted); code != http.StatusCreated {
		t.Fatalf("mint CLI token = %d", code)
	}
	t.Logf("setup: %s", minted.Setup)
	t.Logf("run:   %s", minted.Run)

	home := t.TempDir()
	checkout := filepath.Join(home, "checkout")
	e.sh(home, home, "", "git clone -q --filter=blob:none --no-checkout "+e.repo+" "+checkout)
	e.sh(home, checkout, "", "git checkout -q -B main "+e.sha)
	e.sh(home, checkout, minted.Token, minted.Setup)

	pipeline := envOr("SPARKWING_E2E_PIPELINE", "weather-report")
	runCommand := strings.Replace(minted.Run, "<pipeline>", pipeline, 1) + " --detach"
	runID := strings.TrimSpace(e.sh(home, checkout, "", runCommand))

	var trig struct {
		RepoURL string `json:"repo_url"`
		GitSHA  string `json:"git_sha"`
		Source  string `json:"trigger_source"`
	}
	if code := e.f.call("GET", "/api/v1/triggers/"+runID, e.alice.auth, nil, &trig); code != http.StatusOK {
		t.Fatalf("read trigger %q = %d", runID, code)
	}
	t.Logf("trigger %s: repo_url=%s git_sha=%s source=%s", runID, trig.RepoURL, trig.GitSHA, trig.Source)
	got, _ := sourceurl.Identity(trig.RepoURL)
	want, _ := sourceurl.Identity(e.repo)
	if trig.GitSHA != e.sha || got == "" || got != want {
		t.Fatalf("trigger = %+v, want the checkout's repository at %s", trig, e.sha)
	}

	e.startRunner("runner", e.command)
	e.awaitSuccess(runID)
}

// TestDashboardBranchTriggerRunsOnADirectSourceRunner sends the body the
// dashboard's run form sends when teams are enabled: a repository and a
// branch with no commit, which a direct runner resolves to the branch tip.
// SPARKWING_E2E_BRANCH names the branch (default main).
func TestDashboardBranchTriggerRunsOnADirectSourceRunner(t *testing.T) {
	e := newDirectSourceE2E(t)
	var triggered struct {
		RunID string `json:"run_id"`
	}
	body := map[string]any{
		"pipeline": envOr("SPARKWING_E2E_PIPELINE", "weather-report"),
		"args":     map[string]string{},
		"trigger":  map[string]any{"source": "dashboard"},
		"git":      map[string]any{"repo_url": strings.TrimSuffix(e.repo, ".git"), "branch": envOr("SPARKWING_E2E_BRANCH", "main")},
	}
	if code := e.f.call("POST", "/api/v1/triggers", e.alice.auth, body, &triggered); code != http.StatusAccepted {
		t.Fatalf("trigger = %d", code)
	}
	e.startRunner("runner", e.command)
	e.awaitSuccess(triggered.RunID)
}

// sh runs command in dir the way a person runs it in a terminal, with home
// as the private home directory, feeding it stdin, and returns stdout.
func (e *directSourceE2E) sh(home, dir, stdin, command string) string {
	t := e.t
	t.Helper()
	cmd := exec.CommandContext(e.ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PATH="+e.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"SPARKWING_HOME="+filepath.Join(home, ".sparkwing"),
		"SPARKWING_PROFILES="+filepath.Join(home, "profiles.yaml"),
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\nstdout:\n%s\nstderr:\n%s", command, err, stdout.String(), stderr.String())
	}
	t.Logf("$ %s\n%s%s", command, stdout.String(), stderr.String())
	return stdout.String()
}

type directSourceE2E struct {
	t       *testing.T
	f       *identityFixture
	alice   signedIn
	command string
	binDir  string
	repo    string
	sha     string
	logDir  string
	ctx     context.Context
}

func newDirectSourceE2E(t *testing.T) *directSourceE2E {
	t.Helper()
	binDir := os.Getenv("SPARKWING_E2E_RUNNER_DIR")
	if binDir == "" {
		t.Skip("SPARKWING_E2E_RUNNER_DIR names no directory holding sparkwing-runner")
	}
	sha := os.Getenv("SPARKWING_E2E_SHA")
	if sha == "" {
		t.Skip("SPARKWING_E2E_SHA names no pushed commit")
	}
	e := &directSourceE2E{
		t: t, binDir: binDir, sha: sha,
		repo:   envOr("SPARKWING_E2E_REPO", "https://github.com/sparkwing-dev/sparkwing.git"),
		logDir: envOr("SPARKWING_E2E_LOG_DIR", t.TempDir()),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	e.ctx = ctx
	e.f = newIdentityFixture(t)
	e.alice = e.f.user("alice", "alice@example.com")
	var minted struct {
		Command string `json:"command"`
	}
	if code := e.f.call("POST", "/api/v1/team/runner-tokens", e.alice.auth,
		map[string]any{"name": "alice-laptop", "repos": []string{envOr("SPARKWING_E2E_ALLOW_REPO", "github.com/sparkwing-dev/sparkwing")}},
		&minted); code != http.StatusCreated {
		t.Fatalf("mint runner token = %d", code)
	}
	e.command = minted.Command
	t.Logf("advertised command: %s", redactToken(minted.Command))
	return e
}

func (e *directSourceE2E) trigger(pipeline string) string {
	var triggered struct {
		RunID string `json:"run_id"`
	}
	body := map[string]any{
		"pipeline": pipeline,
		"trigger":  map[string]any{"source": "e2e-direct-source"},
		"git":      map[string]any{"branch": "main", "sha": e.sha, "repo_url": e.repo},
	}
	if code := e.f.call("POST", "/api/v1/triggers", e.alice.auth, body, &triggered); code != http.StatusAccepted {
		e.t.Fatalf("trigger = %d", code)
	}
	return triggered.RunID
}

// startRunner runs command the way a person pastes it into a shell. exec env
// hands the process to the runner, so stopping it stops the runner itself.
func (e *directSourceE2E) startRunner(name, command string) {
	t := e.t
	t.Logf("%s: %s", name, redactToken(command))
	cmd := exec.CommandContext(e.ctx, "sh", "-c", "exec env "+command)
	cmd.Env = append(os.Environ(),
		"PATH="+e.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SPARKWING_HOME="+filepath.Join(t.TempDir(), ".sparkwing"),
	)
	logPath := filepath.Join(e.logDir, t.Name()+"-"+name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = logFile.Close()
		t.Logf("%s log: %s", name, logPath)
	})
}

func (e *directSourceE2E) awaitSuccess(runID string) {
	seen := []string{}
	for {
		state := "trigger=" + triggerStatus(e.f, e.alice, runID) + " run=" + runStatus(e.f, e.alice, runID)
		if len(seen) == 0 || seen[len(seen)-1] != state {
			seen = append(seen, state)
			e.t.Logf("run %s: %s", runID, state)
		}
		if strings.HasSuffix(state, "run=success") {
			return
		}
		if strings.HasSuffix(state, "run=failed") || strings.HasSuffix(state, "run=cancelled") {
			e.t.Fatalf("run %s ended %s", runID, state)
		}
		select {
		case <-e.ctx.Done():
			e.t.Fatalf("run %s never finished; saw %v", runID, seen)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func triggerStatus(f *identityFixture, who signedIn, id string) string {
	var out struct {
		Status string `json:"status"`
	}
	if code := f.call("GET", "/api/v1/triggers/"+id, who.auth, nil, &out); code != http.StatusOK {
		return "http-" + http.StatusText(code)
	}
	return out.Status
}

func runStatus(f *identityFixture, who signedIn, id string) string {
	var out struct {
		Status string `json:"status"`
	}
	if code := f.call("GET", "/api/v1/runs/"+id, who.auth, nil, &out); code != http.StatusOK {
		return "none"
	}
	return out.Status
}

func redactToken(cmd string) string {
	name, rest, ok := strings.Cut(cmd, " ")
	if !ok || !strings.HasPrefix(name, "SPARKWING_AGENT_TOKEN=") {
		return cmd
	}
	return "SPARKWING_AGENT_TOKEN=<redacted> " + rest
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

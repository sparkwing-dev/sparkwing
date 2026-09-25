package cluster

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// The operator's runners build its private repositories, which the cache
// clones over SSH with its own key. A runner reaches the cache only with the
// run's grant, so this drives one operator run end to end: the controller
// mints a grant for the run's team, the runner registers and clones the SSH
// mirror with it, compiles the pipeline it holds and runs it. Another team's
// grant from the same key then cannot clone that mirror.
func TestOperatorRunBuildsFromAnSSHMirrorThroughItsGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: clones over a stand-in SSH transport and compiles a pipeline")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv(authwire.CacheTokenEnv, "")
	t.Setenv(authwire.CacheGrantEnv, "")
	t.Setenv(authwire.CacheGrantKeyEnv, cacheGrantKey)

	origins := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	if err := syscall.Mkfifo(marker, 0o600); err != nil {
		t.Fatal(err)
	}
	sha := makePrivateOrigin(t, filepath.Join(origins, "acme", "private.git"), marker)
	const repoURL = "ssh://git@git.example.invalid/acme/private.git"
	standInSSH(t, origins)

	cacheSrv := newGrantCache(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctrlSrv := httptest.NewServer(controller.New(st, nil).WithCacheCredentials(cacheSrv.URL, operatorCacheToken).Handler())
	t.Cleanup(ctrlSrv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "operator-run", Pipeline: "deploy", RepoURL: repoURL, GitBranch: "main", GitSHA: sha,
	}); err != nil {
		t.Fatal(err)
	}

	loopDone := make(chan error, 1)
	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	go func() {
		loopDone <- RunTriggerLoop(loopCtx, TriggerLoopOptions{
			ControllerURL: ctrlSrv.URL,
			GitcacheURL:   cacheSrv.URL,
			WorkRoot:      t.TempDir(),
			Poll:          50 * time.Millisecond,
			Logger:        discardLogger(),
			MaxConcurrent: 1,
		})
	}()
	ran := make(chan []byte, 1)
	go func() {
		got, _ := os.ReadFile(marker)
		ran <- got
	}()
	t.Cleanup(func() {
		// safety: a run that never came leaves the reader parked on the fifo; a writer that closes frees it.
		if f, err := os.OpenFile(marker, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})
	select {
	case got := <-ran:
		if !strings.Contains(string(got), "handle-trigger") || !strings.Contains(string(got), "operator-run") {
			t.Fatalf("the pipeline ran with %q, want the handle-trigger call for operator-run", got)
		}
	case err := <-loopDone:
		t.Fatalf("trigger loop stopped before the pipeline ran: %v", err)
	case <-ctx.Done():
		t.Fatal("the operator's pipeline never ran from its SSH mirror")
	}
	stopLoop()
	if err := <-loopDone; err != nil {
		t.Fatalf("trigger loop: %v", err)
	}

	other, err := authwire.MintCacheGrant(cacheGrantKey, "team-a", "team-a-run", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bincache.FetchPipelineSourceWithCredentials(ctx, cacheSrv.URL, ctrlSrv.URL, "", other,
		repoURL, "main", sha, t.TempDir())
	if err == nil {
		t.Fatal("another team's grant cloned the operator's SSH mirror")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("another team's grant failed with %v, want the cache's 404 for a mirror it may not read", err)
	}
}

func makePrivateOrigin(t *testing.T, bare, marker string) string {
	t.Helper()
	work := t.TempDir()
	pipeline := filepath.Join(work, ".sparkwing")
	if err := os.MkdirAll(pipeline, 0o755); err != nil {
		t.Fatal(err)
	}
	main := fmt.Sprintf(`package main

import (
	"os"
	"strings"
)

func main() {
	if err := os.WriteFile(%q, []byte(strings.Join(os.Args[1:], " ")), 0o644); err != nil {
		os.Exit(1)
	}
}
`, marker)
	files := map[string]string{
		"go.mod":  "module example.com/pipeline\n\ngo 1.22\n",
		"main.go": main,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(pipeline, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(work, "init", "--quiet", "--initial-branch=main")
	git(work, "add", ".")
	git(work, "commit", "--quiet", "-m", "pipeline")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	git(work, "clone", "--quiet", "--bare", work, bare)
	return git(work, "rev-parse", "HEAD")
}

func standInSSH(t *testing.T, root string) {
	t.Helper()
	// hack: git runs the remote command through this script instead of sshd, so the cache's ssh:// clone path
	// runs for real with no daemon; "simple" keeps git from probing the script for OpenSSH options.
	script := filepath.Join(t.TempDir(), "ssh")
	body := "#!/bin/sh\n" +
		"for last; do :; done\n" +
		"exec sh -c \"$(printf '%s' \"$last\" | sed \"s#'/#'" + root + "/#\")\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", script)
	t.Setenv("GIT_SSH_VARIANT", "simple")
}

// A node the dispatcher hands to a Kubernetes Job runs in a pod that holds the
// runner token and the claim, but no cache credential: the pod runs the team's
// code. So the pod asks the controller for its run's grant itself, and the
// node's source comes from the operator's SSH mirror through that grant.
func TestDispatchedNodeFetchesAnSSHMirrorThroughItsOwnGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: clones over a stand-in SSH transport and compiles a pipeline")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, name := range []string{
		wingwire.APISocketEnv, "SPARKWING_CONTROLLER_URL", "SPARKWING_LOGS_URL",
		"SPARKWING_RUN_ID", "SPARKWING_NODE_ID", "SPARKWING_CACHE_URL",
		authwire.CacheTokenEnv, authwire.CacheGrantEnv,
	} {
		t.Setenv(name, "")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv(authwire.CacheGrantKeyEnv, cacheGrantKey)

	origins := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	sha := makePrivateOrigin(t, filepath.Join(origins, "acme", "private.git"), marker)
	const repoURL = "ssh://git@git.example.invalid/acme/private.git"
	standInSSH(t, origins)

	cacheSrv := newGrantCache(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctrlSrv := httptest.NewServer(controller.New(st, discardLogger()).
		WithCacheCredentials(cacheSrv.URL, operatorCacheToken).EnableAuthFromStore().Handler())
	t.Cleanup(ctrlSrv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const runID, nodeID = "operator-run", "build"
	if err := st.CreateTriggerWithRun(ctx,
		store.Trigger{ID: runID, Pipeline: "deploy", RepoURL: repoURL, GitBranch: "main", GitSHA: sha},
		store.Run{ID: runID, Pipeline: "deploy", Status: "running", StartedAt: time.Now()},
	); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatal(err)
	}
	token, _, err := st.CreateToken("pool", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeRunsRead,
		controller.ScopeRunsState, controller.ScopeRunsWrite,
	}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := client.NewWithToken(ctrlSrv.URL, nil, token).
		ClaimNodeByID(ctx, runID, nodeID, "k8s-job:sw-1", time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	// The pod's environment as the Job spec sets it, less the grant.
	t.Setenv("SPARKWING_AGENT_TOKEN", token)
	t.Setenv("SPARKWING_GITCACHE_URL", cacheSrv.URL)
	t.Setenv("SPARKWING_NODE_CLAIM_HOLDER", claimed.ClaimedBy)
	t.Setenv("SPARKWING_NODE_CLAIM_GENERATION", strconv.FormatInt(claimed.ClaimGeneration, 10))
	t.Setenv("SPARKWING_NODE_CLAIM_MEMBERSHIP", claimed.ClaimMembershipID)
	t.Setenv("SPARKWING_NODE_CLAIM_RESERVATION", claimed.ReservationID)
	t.Setenv("SPARKWING_NODE_CLAIM_LEASE_SECONDS", "600")

	if err := orchestrator.RunNodeCommand([]string{"--controller", ctrlSrv.URL, "--logs", "", runID, nodeID}); err != nil {
		t.Fatalf("run-node: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the node's pipeline never ran: %v", err)
	}
	if want := "run-node " + runID + " " + nodeID; string(got) != want {
		t.Fatalf("the pipeline ran with %q, want %q", got, want)
	}
}

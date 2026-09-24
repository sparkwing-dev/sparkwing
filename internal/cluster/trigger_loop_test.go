package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func init() {
	if os.Getenv("SPARKWING_TRIGGER_LOOP_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("SPARKWING_TRIGGER_LOOP_READY"), []byte("ready"), 0o600); err != nil {
		os.Exit(2)
	}
	_, _ = listener.Accept()
	os.Exit(0)
}

func TestTriggerRunnerArgsK8s(t *testing.T) {
	got := triggerRunnerArgs(TriggerLoopOptions{
		RunnerKind:    "k8s",
		K8sNamespace:  "sparkwing",
		K8sImage:      "example.com/sparkwing-runner:v1",
		K8sRunnerSA:   "runner-job",
		K8sPullSecret: "pull-secret",
		K8sCtrlURL:    "http://controller:4343",
		K8sLogsURL:    "http://logs:4344",
		Kubeconfig:    "/tmp/kubeconfig",
		ArtifactStore: "http://cache:4344",
		K8sLabels: []string{
			"cluster",
		},
		K8sNodeSelector: []string{
			"sparkwing.io/node-pool=runner",
		},
		K8sTolerations: []string{
			"sparkwing.io/node-pool=runner:NoSchedule",
		},
		DependencyProxy:    "http://cache:80",
		K8sImagePullPolicy: "Always",
	})
	want := []string{
		"--runner", "k8s",
		"--namespace", "sparkwing",
		"--image", "example.com/sparkwing-runner:v1",
		"--runner-sa", "runner-job",
		"--image-pull-secret", "pull-secret",
		"--runner-controller-url", "http://controller:4343",
		"--runner-logs-url", "http://logs:4344",
		"--kubeconfig", "/tmp/kubeconfig",
		"--artifact-store", "http://cache:4344",
		"--image-pull-policy", "Always",
		"--dependency-proxy", "http://cache:80",
		"--runner-label", "cluster",
		"--runner-node-selector", "sparkwing.io/node-pool=runner",
		"--runner-toleration", "sparkwing.io/node-pool=runner:NoSchedule",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("triggerRunnerArgs() = %#v, want %#v", got, want)
	}
}

func TestTriggerRunnerArgsForwardsDependencyProxyOptOut(t *testing.T) {
	got := triggerRunnerArgs(TriggerLoopOptions{
		RunnerKind:   "k8s",
		K8sNamespace: "sparkwing",
		K8sImage:     "example.com/sparkwing-runner:v1",
	})
	idx := slices.Index(got, "--dependency-proxy")
	if idx == -1 || idx+1 >= len(got) || got[idx+1] != "off" {
		t.Fatalf("triggerRunnerArgs() = %#v, want --dependency-proxy off", got)
	}
	if slices.Contains(got, "--image-pull-policy") {
		t.Fatalf("triggerRunnerArgs() = %#v, want no --image-pull-policy when unset", got)
	}
}

func TestTriggerRunnerArgsWarm(t *testing.T) {
	got := triggerRunnerArgs(TriggerLoopOptions{
		RunnerKind:   "warm",
		K8sNamespace: "sparkwing",
		K8sImage:     "example.com/sparkwing-runner:v1",
	})
	want := []string{
		"--runner", "warm",
		"--namespace", "sparkwing",
		"--image", "example.com/sparkwing-runner:v1",
		"--dependency-proxy", "off",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("triggerRunnerArgs() = %#v, want %#v", got, want)
	}
}

func TestTriggerRunnerArgsDefaultInProcess(t *testing.T) {
	if got := triggerRunnerArgs(TriggerLoopOptions{}); len(got) != 0 {
		t.Fatalf("triggerRunnerArgs(default) = %#v, want empty", got)
	}
}

func TestHandleTriggerArgsPutFlagsBeforeTriggerID(t *testing.T) {
	got := handleTriggerArgs("trigger-1", TriggerLoopOptions{
		ControllerURL: "http://controller:4343",
		Token:         "token",
		RunnerKind:    "k8s",
		K8sNamespace:  "sparkwing",
		K8sImage:      "example.com/sparkwing-runner:v1",
	})
	triggerIdx := slices.Index(got, "trigger-1")
	runnerIdx := slices.Index(got, "--runner")
	if triggerIdx == -1 || runnerIdx == -1 {
		t.Fatalf("handleTriggerArgs() = %#v, want trigger id and --runner", got)
	}
	if runnerIdx > triggerIdx {
		t.Fatalf("handleTriggerArgs() = %#v, want flags before trigger id", got)
	}
	if got[len(got)-1] != "trigger-1" {
		t.Fatalf("handleTriggerArgs() last arg = %q, want trigger id", got[len(got)-1])
	}
	if slices.Contains(got, "--token") || slices.Contains(got, "token") {
		t.Fatalf("handleTriggerArgs() = %#v, want the bearer off argv", got)
	}
}

func TestRunTriggerLoopClaimsWhileHandlerInFlight(t *testing.T) {
	oldBaked := BakedBinary
	BakedBinary = os.Args[0]
	t.Cleanup(func() { BakedBinary = oldBaked })
	t.Setenv("SPARKWING_TRIGGER_LOOP_HELPER", "1")
	ready := filepath.Join(t.TempDir(), "helper-ready")
	t.Setenv("SPARKWING_TRIGGER_LOOP_READY", ready)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var claims atomic.Int32
	var mu sync.Mutex
	claimTimes := make([]time.Time, 0, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/claim":
			n := claims.Add(1)
			if n > 2 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if n == 2 {
				if err := waitForTriggerHelper(ready, 15*time.Second); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
			mu.Lock()
			claimTimes = append(claimTimes, time.Now())
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(store.Trigger{
				ID:            "trigger-" + string(rune('0'+n)),
				Pipeline:      "demo",
				TriggerSource: "test",
				Status:        "claimed",
			})
			if n == 2 {
				cancel()
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/trigger-1/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]bool{"cancel_requested": false})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/trigger-2/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]bool{"cancel_requested": false})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	err := RunTriggerLoop(ctx, TriggerLoopOptions{
		ControllerURL: srv.URL,
		GitcacheURL:   srv.URL,
		WorkRoot:      t.TempDir(),
		Poll:          10 * time.Millisecond,
		MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatalf("RunTriggerLoop: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(claimTimes) < 2 {
		t.Fatalf("claims = %d, want at least 2", len(claimTimes))
	}
	// safety: the in-flight handler blocks forever, so any bounded return proves the loop did not wait on it
	if elapsed := time.Since(claimTimes[1]); elapsed >= 5*time.Second {
		t.Fatalf("trigger loop returned %s after the second claim, want < 5s", elapsed)
	}
}

func TestRunTriggerLoop_EarlyFailureFinishesWithClaimGeneration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var finishRunGeneration, finishTriggerGeneration string
	var claimed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/claim":
			if claimed.Swap(true) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(store.Trigger{
				ID: "bad-source", Pipeline: "demo", RepoURL: "http://127.0.0.1/repo",
				Status: "claimed", ClaimSeq: 7,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs/bad-source/finish":
			finishRunGeneration = r.Header.Get(store.TriggerGenerationHeader)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/bad-source/done":
			finishTriggerGeneration = r.Header.Get(store.TriggerGenerationHeader)
			w.WriteHeader(http.StatusNoContent)
			cancel()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if err := RunTriggerLoop(ctx, TriggerLoopOptions{
		ControllerURL: srv.URL, GitcacheURL: srv.URL, WorkRoot: t.TempDir(),
		Poll: 5 * time.Millisecond, MaxConcurrent: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if finishRunGeneration != "7" || finishTriggerGeneration != "7" {
		t.Fatalf("failure cleanup generations = run %q trigger %q, want 7 for both",
			finishRunGeneration, finishTriggerGeneration)
	}
}

func TestRunTriggerLoop_DoesNotCloseTriggerWhenFailureCannotBeRecorded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var claimed atomic.Bool
	var done atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/claim":
			if claimed.Swap(true) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(store.Trigger{
				ID: "bad-source", Pipeline: "demo", RepoURL: "http://127.0.0.1/repo",
				Status: "claimed", ClaimSeq: 7,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs/bad-source/finish":
			http.Error(w, "state write unavailable", http.StatusServiceUnavailable)
			cancel()
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/bad-source/done":
			done.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if err := RunTriggerLoop(ctx, TriggerLoopOptions{
		ControllerURL: srv.URL, GitcacheURL: srv.URL, WorkRoot: t.TempDir(),
		Poll: 5 * time.Millisecond, MaxConcurrent: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if !claimed.Load() || done.Load() != 0 {
		t.Fatalf("claim = %t, finished trigger after failed run write = %d", claimed.Load(), done.Load())
	}
}

func waitForTriggerHelper(path string, timeout time.Duration) error {
	deadlineAt := time.Now().Add(timeout)
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
	for {
		if !time.Now().Before(deadlineAt) {
			return fmt.Errorf("trigger helper did not publish readiness within %s", timeout)
		}
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read trigger helper readiness: %w", err)
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			return fmt.Errorf("trigger helper did not publish readiness within %s", timeout)
		}
	}
}

// A metered credential's trigger claims are all refused while the loop runs
// nodes in its own process, so the loop stops and says why instead of
// polling a refusal forever.
func TestRunTriggerLoopStopsWhenMeteredInProcessClaimsAreRefused(t *testing.T) {
	var nodeRunner atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/triggers/claim" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var body struct {
			NodeRunner string `json:"node_runner"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		nodeRunner.Store(body.NodeRunner)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"metered_inprocess_nodes","message":"refused"}`))
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := RunTriggerLoop(ctx, TriggerLoopOptions{
		ControllerURL: ts.URL, GitcacheURL: ts.URL, WorkRoot: t.TempDir(),
		Poll: time.Millisecond, MaxConcurrent: 1, Logger: discardLogger(),
	})
	if !errors.Is(err, store.ErrMeteredInProcessNodes) {
		t.Fatalf("RunTriggerLoop = %v, want it to stop with ErrMeteredInProcessNodes", err)
	}
	if got, _ := nodeRunner.Load().(string); got != "inprocess" {
		t.Fatalf("the claim named node runner %q, want inprocess", got)
	}
}

// Against a controller older than the claim filter a direct-source runner
// claims without its list, and fails a run outside the list naming the list,
// before anything is fetched.
func TestRunTriggerLoop_DirectSourceRefusesARepositoryOutsideTheAllowlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var reason string
	var sentAllow []string
	var claimed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/claim":
			var claim struct {
				AllowRepos []string `json:"allow_repos"`
			}
			_ = json.NewDecoder(r.Body).Decode(&claim)
			sentAllow = claim.AllowRepos
			if claimed.Swap(true) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(store.Trigger{
				ID: "evil", Pipeline: "demo", Status: "claimed", ClaimSeq: 1,
				RepoURL: "https://git.invalid/evil/payload.git",
				GitSHA:  "0123456789abcdef0123456789abcdef01234567",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs/evil/finish":
			var body struct {
				Error string `json:"error"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			reason = body.Error
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/evil/done":
			w.WriteHeader(http.StatusNoContent)
			cancel()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	allow, err := sourceurl.ParseRepoAllowlist([]string{"github.com/acme/*"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RunTriggerLoop(ctx, TriggerLoopOptions{
		ControllerURL: srv.URL, AllowRepos: allow, WorkRoot: t.TempDir(),
		Poll: 5 * time.Millisecond, MaxConcurrent: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// safety: this controller advertises no claim filter, so the runner claims
	// without the field and its own refusal is what stops the run.
	if sentAllow != nil {
		t.Fatalf("claims carried allow_repos %q to a controller that does not advertise it", sentAllow)
	}
	if !strings.Contains(reason, "github.com/acme/*") || !strings.Contains(reason, "git.invalid/evil/payload") {
		t.Fatalf("run finished with %q, want a refusal naming the repository and the allowlist", reason)
	}
	if _, statErr := os.Stat(filepath.Join(home, "source-direct")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused run still reached the fetch: %v", statErr)
	}
}

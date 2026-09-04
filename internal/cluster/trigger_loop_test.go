package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

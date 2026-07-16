package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
		"--runner-node-selector", "sparkwing.io/node-pool=runner",
		"--runner-toleration", "sparkwing.io/node-pool=runner:NoSchedule",
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
}

func TestRunTriggerLoopClaimsWhileHandlerInFlight(t *testing.T) {
	if os.Getenv("SPARKWING_TRIGGER_LOOP_HELPER") == "1" {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}

	oldBaked := BakedBinary
	BakedBinary = os.Args[0]
	t.Cleanup(func() { BakedBinary = oldBaked })
	t.Setenv("SPARKWING_TRIGGER_LOOP_HELPER", "1")

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
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/trigger-1/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]bool{"cancel_requested": false})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/trigger-2/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]bool{"cancel_requested": false})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()

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
	if gap := claimTimes[1].Sub(claimTimes[0]); gap > 250*time.Millisecond {
		t.Fatalf("second claim gap = %s, want concurrent claim while first handler is running", gap)
	}
}

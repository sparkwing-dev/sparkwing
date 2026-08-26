package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestProcessPerNode_BounceRestartsANodeWithoutFailingTheRun is the
// operator story end to end: a job is wedged, someone bounces it, and
// the run they did not want to lose finishes.
//
// The whole claim rests on what the node row does NOT do. Killing a
// node process is otherwise indistinguishable from a crash, and a
// crash without a terminal row is a failure -- which would wake the
// dispatcher's waiters and cascade into everything downstream. So the
// assertions here are: the node ran twice in two processes, no
// terminal row appeared between them, the consumer got the second
// attempt's output, and the run ended green. The accounting is the
// other half: the machine paid for the killed attempt too, and the
// node's charge has to say so.
func TestProcessPerNode_BounceRestartsANodeWithoutFailingTheRun(t *testing.T) {
	mod, bin := buildProcPerNodeBinary(t)
	cli := wingdHostBin(t)

	home := t.TempDir()
	stopHomeDaemon(t, home)
	probe := t.TempDir()
	runEnv := append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_WINGD_BIN="+cli,
		"SPARKWING_LOG_FORMAT=json",
		"PROC_PROBE_DIR="+probe,
	)

	cmd := exec.Command(bin, "bounceproof")
	cmd.Dir = mod
	cmd.Env = runEnv
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start run: %v", err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	runID := waitForProbeFile(t, probe, "run.id", 120*time.Second)
	waitForAttempts(t, probe, 1, 120*time.Second)

	bounceOut := runBin(t, mod, runEnv, cli,
		"runs", "bounce", "--run", runID, "--node", "work", "--home", home)
	if !strings.Contains(bounceOut, "bounce requested") {
		t.Fatalf("bounce verb said:\n%s", bounceOut)
	}
	t.Logf("bounce verb output:\n%s", bounceOut)

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run failed after the bounce: %v\n%s", err, out.String())
		}
	case <-time.After(180 * time.Second):
		t.Fatalf("run never finished after the bounce\n%s", out.String())
	}

	attempts := attemptPIDs(t, probe)
	if len(attempts) != 2 {
		t.Fatalf("node ran %d time(s) (%v); one bounce is one kill and one re-run", len(attempts), attempts)
	}
	if attempts[0] == attempts[1] {
		t.Errorf("both attempts report pid %s; the node was not re-run in a fresh process", attempts[0])
	}
	if !strings.Contains(out.String(), "consumed attempt=2") {
		t.Errorf("downstream did not consume the second attempt's output:\n%s", out.String())
	}

	st, err := store.Open(orchestrator.PathsAt(home).StateDB())
	if err != nil {
		t.Fatalf("open runs store: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != "success" {
		t.Errorf("run status = %q, want success: a bounce must not cost the run", run.Status)
	}

	work, err := st.GetNode(ctx, runID, "work")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if work.Outcome != string(sparkwing.Success) {
		t.Fatalf("bounced node outcome = %q (%s), want success", work.Outcome, work.Error)
	}
	after, err := st.GetNode(ctx, runID, "after")
	if err != nil || after.Outcome != string(sparkwing.Success) {
		t.Fatalf("downstream node = %+v (%v), want success", after, err)
	}

	// The killed attempt burned 1.5s of CPU before it went to sleep; the
	// attempt that replaced it burns a fraction of that. A charge that
	// covered only the survivor could not reach this floor, so the node
	// would be priced as though the machine never ran the first attempt.
	if work.CPUNanos < int64(time.Second) {
		t.Errorf("node cpu_nanos = %s, want the killed attempt's CPU included (it spun 1.5s)",
			time.Duration(work.CPUNanos))
	}
	// started_at is re-stamped by the surviving attempt, so the node's own
	// window measures that attempt alone while the accounting spans both.
	if work.StartedAt == nil || work.FinishedAt == nil {
		t.Fatalf("node timestamps = %v/%v, want both stamped", work.StartedAt, work.FinishedAt)
	}
	surviving := work.FinishedAt.Sub(*work.StartedAt)
	if time.Duration(work.ProcessWallNanos) <= surviving {
		t.Errorf("process_wall_nanos = %s but the surviving attempt alone took %s; the killed attempt's span was dropped",
			time.Duration(work.ProcessWallNanos), surviving)
	}

	rows, err := st.ListNodeBounces(ctx, runID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListNodeBounces = %+v, %v; want the one request", rows, err)
	}
	if rows[0].ConsumedAt == nil || rows[0].Outcome != store.BounceBounced {
		t.Errorf("request row = %+v, want it consumed as %q", rows[0], store.BounceBounced)
	}

	events, err := st.ListEventsAfter(ctx, runID, 0, 1000)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var bounced *store.Event
	for i, ev := range events {
		if ev.Kind == "node_bounced" && ev.NodeID == "work" {
			bounced = &events[i]
		}
	}
	if bounced == nil {
		t.Fatal("no node_bounced event on the node; the restart is not in the run's history")
	}
	var attrs map[string]any
	if err := json.Unmarshal(bounced.Payload, &attrs); err != nil {
		t.Fatalf("decode node_bounced payload: %v", err)
	}
	if attrs["admission_lease_retained"] != true {
		t.Errorf("node_bounced attrs = %v, want admission_lease_retained", attrs)
	}
	if attrs["requested_by"] == "" || attrs["requested_by"] == nil {
		t.Errorf("node_bounced attrs = %v, want the requester recorded", attrs)
	}
}

// waitForProbeFile blocks until the named probe file has content and
// returns it trimmed.
func waitForProbeFile(t *testing.T, dir, name string, timeout time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, name)
	deadline := time.Now().Add(timeout)
	for {
		if raw, err := os.ReadFile(path); err == nil {
			if text := strings.TrimSpace(string(raw)); text != "" {
				return text
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared within %s", name, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// attemptPIDs returns the pid of every attempt of the bounced node
// recorded so far.
func attemptPIDs(t *testing.T, probe string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(probe, "bounce-attempts"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}

// waitForAttempts blocks until at least n attempts have started.
func waitForAttempts(t *testing.T, probe string, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if len(attemptPIDs(t, probe)) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d attempt(s) within %s, want %d", len(attemptPIDs(t, probe)), timeout, n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

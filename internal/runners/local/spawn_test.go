//go:build unix

package local

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func fakeNodeBinary(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-pipeline")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

type spawnFixture struct {
	runner *Runner
	store  *store.Store
	ctrl   *client.Client

	url string
}

func newSpawnFixture(t *testing.T, exe string, tune ...func(*Config)) *spawnFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).Handler())
	t.Cleanup(srv.Close)

	ctrl := client.NewWithToken(srv.URL, nil, "")
	cfg := Config{
		Executable:       exe,
		ControllerURL:    srv.URL,
		WorkDir:          t.TempDir(),
		Home:             t.TempDir(),
		TerminationGrace: time.Second,

		SuperviseInterval: 25 * time.Millisecond,
		Logger:            quiet,
	}
	for _, fn := range tune {
		fn(&cfg)
	}
	return &spawnFixture{runner: New(ctrl, cfg), store: st, ctrl: ctrl, url: srv.URL}
}

func (f *spawnFixture) seedNode(t *testing.T, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := f.store.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := f.store.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
}

func TestRunNode_TerminalRowWinsOverExitStatus(t *testing.T) {
	f := newSpawnFixture(t, fakeNodeBinary(t, "exit 0"))
	f.seedNode(t, "run-1", "build")
	ctx := context.Background()
	if err := f.store.FinishNode(ctx, "run-1", "build",
		string(sparkwing.Success), "", []byte(`{"digest":"abc"}`)); err != nil {
		t.Fatalf("finish node: %v", err)
	}

	res := f.runner.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
	}
	out, ok := res.Output.([]byte)
	if !ok || string(out) != `{"digest":"abc"}` {
		t.Fatalf("Output = %#v, want the row's raw bytes", res.Output)
	}
	if res.Usage == nil {
		t.Error("Usage is nil; the kernel's accounting for the node process was not captured")
	}
}

func TestRunNode_NonZeroExitSynthesizesFailureAndWritesTheRow(t *testing.T) {
	f := newSpawnFixture(t, fakeNodeBinary(t, "exit 3"))
	f.seedNode(t, "run-2", "build")
	ctx := context.Background()

	res := f.runner.RunNode(ctx, runner.Request{RunID: "run-2", NodeID: "build"})
	if res.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %q, want failed", res.Outcome)
	}
	n, err := f.store.GetNode(ctx, "run-2", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if !runner.NodeTerminal(n) {
		t.Fatalf("node row is %+v; the run would wait on it forever", n)
	}
	if n.ExitCode == nil || *n.ExitCode != 3 {
		t.Errorf("row exit code = %v, want 3", n.ExitCode)
	}
}

func TestRunNode_ForwardsChildStdoutAndStderr(t *testing.T) {
	f := newSpawnFixture(t, fakeNodeBinary(t,
		`echo '{"level":"info","msg":"hello from the node"}'`+"\n"+
			`echo 'raw stdout line'`+"\n"+
			`echo 'stderr line' >&2`+"\n"+
			`exit 0`))
	f.seedNode(t, "run-3", "build")
	ctx := context.Background()
	if err := f.store.FinishNode(ctx, "run-3", "build", string(sparkwing.Success), "", nil); err != nil {
		t.Fatalf("finish node: %v", err)
	}

	cap := &captureLogger{}
	f.runner.RunNode(ctx, runner.Request{RunID: "run-3", NodeID: "build", Delegate: cap})

	var sawRecord, sawRawStdout, sawStderr bool
	for _, r := range cap.records() {
		switch r.Msg {
		case "hello from the node":
			sawRecord = r.Level == "info"
		case "raw stdout line":
			sawRawStdout = r.Level == "warn"
		case "stderr line":
			sawStderr = r.Level == "warn"
		}
	}
	if !sawRecord {
		t.Error("the child's structured log record did not reach the delegate")
	}
	if !sawRawStdout {
		t.Error("the child's unstructured stdout line was dropped")
	}
	if !sawStderr {
		t.Error("the child's stderr line was dropped")
	}
}

func TestRunNode_CancelTerminatesAndReportsCancelled(t *testing.T) {
	f := newSpawnFixture(t, fakeNodeBinary(t, "sleep 60"))
	f.seedNode(t, "run-4", "build")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	start := time.Now()
	res := f.runner.RunNode(ctx, runner.Request{RunID: "run-4", NodeID: "build"})
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("cancel took %s; the process tree was not terminated", elapsed)
	}
	if res.Outcome != sparkwing.Cancelled {
		t.Fatalf("outcome = %q (err=%v), want cancelled", res.Outcome, res.Err)
	}

	n, err := f.store.GetNode(context.Background(), "run-4", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.Outcome == string(sparkwing.Failed) {
		t.Errorf("runner wrote a failure over a cancelled node: %+v", n)
	}
}

func TestRunNode_PassesTheParentLivenessPipeOnFD3(t *testing.T) {

	f := newSpawnFixture(t, fakeNodeBinary(t,
		"if [ -r /dev/fd/3 ]; then echo '{\"level\":\"info\",\"msg\":\"fd3 present\"}'; else exit 9; fi"))
	f.seedNode(t, "run-5", "build")
	ctx := context.Background()
	if err := f.store.FinishNode(ctx, "run-5", "build", string(sparkwing.Success), "", nil); err != nil {
		t.Fatalf("finish node: %v", err)
	}

	cap := &captureLogger{}
	res := f.runner.RunNode(ctx, runner.Request{RunID: "run-5", NodeID: "build", Delegate: cap})
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v); the child did not see fd %d", res.Outcome, res.Err, ParentLivenessFD)
	}
	var saw bool
	for _, r := range cap.records() {
		if r.Msg == "fd3 present" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("child did not report the liveness descriptor on fd %d", ParentLivenessFD)
	}
}

func TestRunNode_OversizedChildLineDoesNotDeadlockTheRun(t *testing.T) {
	f := newSpawnFixture(t, fakeNodeBinary(t,
		"head -c 2097152 /dev/zero | tr '\\0' 'x'\n"+
			"echo\n"+
			"i=0\n"+
			"while [ $i -lt 5000 ]; do echo '{\"level\":\"info\",\"msg\":\"after\"}'; i=$((i+1)); done\n"+
			"exit 0"))
	f.seedNode(t, "run-7", "build")
	ctx := context.Background()
	if err := f.store.FinishNode(ctx, "run-7", "build", string(sparkwing.Success), "", nil); err != nil {
		t.Fatalf("finish node: %v", err)
	}

	cap := &captureLogger{}
	done := make(chan runner.Result, 1)
	go func() {
		done <- f.runner.RunNode(ctx, runner.Request{RunID: "run-7", NodeID: "build", Delegate: cap})
	}()

	select {
	case res := <-done:
		if res.Outcome != sparkwing.Success {
			t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
		}
	case <-time.After(60 * time.Second):

		t.Fatal("RunNode did not return: the forwarder stopped draining and the child is blocked writing")
	}

	var after int
	var sawTruncated bool
	for _, r := range cap.records() {
		switch {
		case r.Msg == "after":
			after++
		case strings.Contains(r.Msg, "truncated"):
			sawTruncated = true
		}
	}
	if !sawTruncated {
		t.Error("the oversized line was dropped rather than truncated")
	}
	if after != 5000 {
		t.Errorf("forwarded %d of the 5000 lines that followed the oversized one", after)
	}
}

func TestRunNode_MissingExecutableFailsWithoutHanging(t *testing.T) {
	f := newSpawnFixture(t, filepath.Join(t.TempDir(), "does-not-exist"))
	f.seedNode(t, "run-6", "build")
	res := f.runner.RunNode(context.Background(), runner.Request{RunID: "run-6", NodeID: "build"})
	if res.Outcome != sparkwing.Failed || res.Err == nil {
		t.Fatalf("outcome = %q, err = %v; want a failure", res.Outcome, res.Err)
	}
}

func TestAdvertisedLabels_DefaultsToLocal(t *testing.T) {
	r := New(nil, Config{Executable: "x"})
	got := r.AdvertisedLabels()
	if len(got) != 1 || got[0] != "local" {
		t.Fatalf("AdvertisedLabels = %v, want [local]", got)
	}
}

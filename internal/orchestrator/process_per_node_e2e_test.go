package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestProcessPerNode_EveryNodeRunsInItsOwnProcess is the parity gate
// for local execution moving out of the dispatcher's goroutines.
//
// It has to build a real pipeline binary, because "can this binary
// re-enter itself at run-node" is the whole property under test and a
// test binary cannot. What it then asserts is the shape of the model:
// each node body runs in a process of its own, a typed output still
// reaches its consumer across that boundary, and a recovery node --
// which the plan never registered by id -- is reachable and runs too.
func TestProcessPerNode_EveryNodeRunsInItsOwnProcess(t *testing.T) {
	mod, bin := buildProcPerNodeBinary(t)

	home := t.TempDir()
	stopHomeDaemon(t, home)
	probe := t.TempDir()
	runEnv := append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_LOG_FORMAT=json",
		"PROC_PROBE_DIR="+probe,
	)

	out := runBin(t, mod, runEnv, bin, "spawnproof")

	dispatcher := readPID(t, probe, "dispatcher")
	for _, node := range []string{"produce", "consume", "recover"} {
		pid := readPID(t, probe, node)
		if pid == dispatcher {
			t.Errorf("node %q ran in the dispatcher's process (%d); local execution is still in-process",
				node, pid)
		}
	}
	// safety: two nodes sharing a process would mean one process served both,
	// which is the model this replaces.
	if readPID(t, probe, "produce") == readPID(t, probe, "consume") {
		t.Error("produce and consume shared a process")
	}

	// safety: the consumer asserts the value itself and fails the node when it
	// is wrong, so a green run is the proof the typed output crossed
	// the boundary.
	if !strings.Contains(out, "consumed digest=sha-abc123") {
		t.Errorf("consumer did not read the producer's typed output:\n%s", out)
	}
}

// TestProcessPerNode_SpawnNodeRunsInsideItsParentsProcess is the
// spawn half of the same parity gate.
//
// SpawnNode used to be served only by the dispatcher, which splices
// the child into its live plan -- so once a node's body moved into its
// own process, the first spawn in a local run failed the way it had
// always failed in a pod: no handler in ctx. This asserts the child
// runs, in its parent's process (it is that node's sub-work, and its
// CPU is charged there), and that the run record carries it as a real
// node: a namespaced row, its typed output, and the parent's
// spawn_dispatched event naming it.
func TestProcessPerNode_SpawnNodeRunsInsideItsParentsProcess(t *testing.T) {
	mod, bin := buildProcPerNodeBinary(t)

	home := t.TempDir()
	stopHomeDaemon(t, home)
	probe := t.TempDir()
	runEnv := append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_LOG_FORMAT=json",
		"PROC_PROBE_DIR="+probe,
	)

	runBin(t, mod, runEnv, bin, "spawnnode")

	dispatcher := readPID(t, probe, "dispatcher")
	parent := readPID(t, probe, "spawn-parent")
	child := readPID(t, probe, "spawn-child")
	if parent == dispatcher {
		t.Errorf("the spawning node ran in the dispatcher's process (%d)", dispatcher)
	}
	if child != parent {
		t.Errorf("spawned child ran in pid %d, its parent in %d; a spawn is the parent node's own sub-work",
			child, parent)
	}

	st, err := store.Open(orchestrator.PathsAt(home).StateDB())
	if err != nil {
		t.Fatalf("open run store: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	runs, err := st.ListRuns(ctx, store.RunFilter{Pipelines: []string{"spawnnode"}})
	if err != nil || len(runs) == 0 {
		t.Fatalf("list runs: %v (%d found)", err, len(runs))
	}
	runID := runs[0].ID

	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	parentRow, childRow := find(nodes, "parent"), find(nodes, "parent/scan")
	if parentRow == nil || childRow == nil {
		t.Fatalf("missing nodes; have %v", nodeIDs(nodes))
	}
	if parentRow.Outcome != string(sparkwing.Success) {
		t.Errorf("parent outcome = %q (err=%q), want success", parentRow.Outcome, parentRow.Error)
	}
	if childRow.Outcome != string(sparkwing.Success) {
		t.Errorf("child outcome = %q (err=%q), want success", childRow.Outcome, childRow.Error)
	}
	if got := string(childRow.Output); got != `{"findings":7}` {
		t.Errorf("child output = %s, want {\"findings\":7}", got)
	}

	events, err := st.ListEventsAfter(ctx, runID, 0, 500)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var dispatched bool
	for _, ev := range events {
		if ev.Kind == "spawn_dispatched" && ev.NodeID == "parent" && string(ev.Payload) == `"parent/scan"` {
			dispatched = true
		}
	}
	if !dispatched {
		t.Error("no spawn_dispatched event on the parent naming parent/scan")
	}
}

// TestProcessPerNode_NodeAbandonsARunWhoseDispatcherDied is the
// orphan guarantee.
//
// A dispatcher killed with SIGKILL sends nothing and runs no
// deferred cleanup, and its node processes live in their own process
// groups precisely so a cancelled node's tree can be signaled
// independently -- which also means they are not swept up when the
// dispatcher dies. Without the liveness pipe they would keep running,
// reparented to init, holding CPU and locks for a run nobody is
// coordinating.
//
// The node here sleeps ten minutes and ignores its context, so
// passing requires the hard exit, not just the cancel.
func TestProcessPerNode_NodeAbandonsARunWhoseDispatcherDied(t *testing.T) {
	mod, bin := buildProcPerNodeBinary(t)

	home := t.TempDir()
	stopHomeDaemon(t, home)
	probe := t.TempDir()

	cmd := exec.Command(bin, "orphanproof")
	cmd.Dir = mod
	cmd.Env = append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_LOG_FORMAT=json",
		"PROC_PROBE_DIR="+probe,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	dispatcherDone := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(dispatcherDone) }()

	nodePID := waitForPID(t, probe, "orphan", 90*time.Second)
	t.Cleanup(func() {
		// safety: a failing run must not leave the sleeper behind for whoever
		// debugs this next.
		if processAlive(nodePID) {
			_ = syscall.Kill(nodePID, syscall.SIGKILL)
		}
	})

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill dispatcher: %v", err)
	}
	<-dispatcherDone

	deadline := time.Now().Add(60 * time.Second)
	for processAlive(nodePID) {
		if time.Now().After(deadline) {
			t.Fatalf("node process %d outlived its dispatcher; the run is orphaned", nodePID)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForPID blocks until the named probe file appears and returns the
// pid it holds.
func waitForPID(t *testing.T, dir, name string, timeout time.Duration) int {
	t.Helper()
	path := filepath.Join(dir, name+".pid")
	deadline := time.Now().Add(timeout)
	for {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, cerr := strconv.Atoi(strings.TrimSpace(string(raw))); cerr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never recorded a pid within %s", name, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// processAlive reports whether pid still names a live process. Signal
// 0 performs the permission and existence checks without delivering
// anything.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// buildProcPerNodeBinary compiles a real pipeline binary against the
// working tree. Only a real binary can re-enter itself at `run-node`,
// which is the property both tests here are about.
func buildProcPerNodeBinary(t *testing.T) (mod, bin string) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the probes read a POSIX process tree")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	repoRoot := repoRootDir(t)

	mod = t.TempDir()
	writeMod(t, filepath.Join(mod, "go.mod"), ""+
		"module procpernode\n\ngo 1.26.0\n\n"+
		"require github.com/sparkwing-dev/sparkwing v0.0.0\n\n"+
		"replace github.com/sparkwing-dev/sparkwing => "+repoRoot+"\n")
	writeMod(t, filepath.Join(mod, "jobs", "jobs.go"), procPerNodeJobs)
	writeMod(t, filepath.Join(mod, "main.go"), procPerNodeMain)

	buildEnv := append(os.Environ(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
	runGo(t, mod, buildEnv, "mod", "tidy")
	bin = filepath.Join(mod, "procpernode")
	runGo(t, mod, buildEnv, "build", "-o", bin, ".")
	return mod, bin
}

func readPID(t *testing.T, dir, name string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name+".pid"))
	if err != nil {
		t.Fatalf("read %s pid probe: %v", name, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse %s pid %q: %v", name, raw, err)
	}
	return pid
}

const procPerNodeJobs = `package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// StampPID records which process ran a given piece of work.
func StampPID(name string) {
	dir := os.Getenv("PROC_PROBE_DIR")
	if dir == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name+".pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

type BuildOut struct {
	Digest string ` + "`json:\"digest\"`" + `
}

type Produce struct {
	sparkwing.Base
	sparkwing.Produces[BuildOut]
}

func (j *Produce) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (BuildOut, error) {
		StampPID("produce")
		return BuildOut{Digest: "sha-abc123"}, nil
	}), nil
}

type Consume struct {
	sparkwing.Base
	Build sparkwing.Ref[BuildOut]
}

func (j *Consume) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) error {
		StampPID("consume")
		got := j.Build.Get(ctx)
		if got.Digest != "sha-abc123" {
			return fmt.Errorf("consumed digest=%q, want sha-abc123", got.Digest)
		}
		sparkwing.Info(ctx, "consumed digest=%s", got.Digest)
		return nil
	}), nil
}

type ScanOut struct {
	Findings int ` + "`json:\"findings\"`" + `
}

type SpawnScan struct {
	sparkwing.Base
	sparkwing.Produces[ScanOut]
}

func (j *SpawnScan) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "scan", func(ctx context.Context) (ScanOut, error) {
		StampPID("spawn-child")
		return ScanOut{Findings: 7}, nil
	}), nil
}

type SpawnParent struct{ sparkwing.Base }

func (j *SpawnParent) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	setup := sparkwing.Step(w, "setup", func(ctx context.Context) error {
		StampPID("spawn-parent")
		return nil
	})
	scan := sparkwing.JobSpawn(w, "scan", &SpawnScan{}).Needs(setup)
	sparkwing.Step(w, "after", func(ctx context.Context) error {
		sparkwing.Info(ctx, "parent resumed after its spawned child")
		return nil
	}).Needs(scan)
	return nil, nil
}

type Spawnnode struct{ sparkwing.Base }

func (p *Spawnnode) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", &SpawnParent{})
	return nil
}

type Orphanproof struct{ sparkwing.Base }

func (p *Orphanproof) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "sleeper", func(ctx context.Context) error {
		StampPID("orphan")
		// Deliberately ignores ctx: cancellation alone must not be what
		// the orphan guarantee rests on.
		time.Sleep(10 * time.Minute)
		return nil
	})
	return nil
}

type Spawnproof struct{ sparkwing.Base }

func (p *Spawnproof) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	produce := sparkwing.Job(plan, "produce", &Produce{})
	sparkwing.Job(plan, "consume", &Consume{Build: sparkwing.RefTo[BuildOut](produce)}).Needs(produce)

	flaky := sparkwing.Job(plan, "flaky", func(ctx context.Context) error {
		return fmt.Errorf("always fails")
	})
	flaky.Optional()
	flaky.OnFailure("recover", func(ctx context.Context) error {
		StampPID("recover")
		return nil
	})
	return nil
}

func init() {
	sparkwing.Register("spawnproof", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Spawnproof{} })
	sparkwing.Register("orphanproof", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Orphanproof{} })
	sparkwing.Register("spawnnode", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Spawnnode{} })
}
`

const procPerNodeMain = `package main

import (
	"os"

	"procpernode/jobs"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
)

func main() {
	// Every node process rebuilds the plan, so the dispatcher has to
	// stamp its identity from the entrypoint that only it reaches.
	if len(os.Args) > 1 && (os.Args[1] == "spawnproof" || os.Args[1] == "orphanproof" || os.Args[1] == "spawnnode") {
		jobs.StampPID("dispatcher")
	}
	runner.Main()
}
`

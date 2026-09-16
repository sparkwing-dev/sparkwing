package orchestrator_test

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestProcessPerNode_EveryNodeRunsInItsOwnProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 5.5s of real work; the fast class runs under -short")
	}
	mod, bin := buildProcPerNodeBinary(t)

	home := t.TempDir()
	stopHomeDaemon(t, home)
	probe := t.TempDir()
	runEnv := append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_WINGD_BIN="+wingdHostBin(t),
		"SPARKWING_LOG_FORMAT=json",
		"SPARKWING_LOG_LEVEL=debug",
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

	if readPID(t, probe, "produce") == readPID(t, probe, "consume") {
		t.Error("produce and consume shared a process")
	}

	if !strings.Contains(out, "consumed digest=sha-abc123") {
		t.Errorf("consumer did not read the producer's typed output:\n%s", out)
	}

	assertNodesRecordedTheirUsage(t, home, "spawnproof", "produce", "consume")
}

func assertNodesRecordedTheirUsage(t *testing.T, home, pipeline string, nodeIDs ...string) {
	t.Helper()
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("open runs store: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	run, err := st.GetLatestRun(ctx, pipeline, nil, time.Hour)
	if err != nil || run == nil {
		t.Fatalf("latest %s run: %v", pipeline, err)
	}
	for _, id := range nodeIDs {
		n, err := st.GetNode(ctx, run.ID, id)
		if err != nil {
			t.Fatalf("node %q: %v", id, err)
		}
		if n.CPUNanos <= 0 {
			t.Errorf("node %q cpu_nanos = %d, want the CPU its process burned", id, n.CPUNanos)
		}
		if n.MaxRSSBytes <= 0 {
			t.Errorf("node %q max_rss_bytes = %d, want the peak RSS its process held", id, n.MaxRSSBytes)
		}
		if n.ProcessWallNanos <= 0 {
			t.Fatalf("node %q process_wall_nanos = %d, want the span the process existed for", id, n.ProcessWallNanos)
		}
		measured := float64(n.CPUNanos) / float64(n.ProcessWallNanos)
		if measured > float64(runtime.NumCPU()) {
			t.Errorf("node %q measured %.2f cores; a %d-core host cannot have given that, so the span is not the one the CPU was drawn over",
				id, measured, runtime.NumCPU())
		}
		prof, err := st.GetPipelineProfile(ctx, pipeline, id)
		if err != nil || prof == nil {
			t.Fatalf("node %q profile missing: %v", id, err)
		}
		if diff := math.Abs(prof.SustainedCores - measured); diff > 0.05*measured {
			t.Errorf("node %q charges %.3f sustained cores but its process measured %.3f (cpu %s over %s)",
				id, prof.SustainedCores, measured,
				time.Duration(n.CPUNanos), time.Duration(n.ProcessWallNanos))
		}
	}
}

func TestProcessPerNode_SpawnNodeRunsInsideItsParentsProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 5.4s of real work; the fast class runs under -short")
	}
	mod, bin := buildProcPerNodeBinary(t)

	home := t.TempDir()
	stopHomeDaemon(t, home)
	probe := t.TempDir()
	runEnv := append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_WINGD_BIN="+wingdHostBin(t),
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

func TestProcessPerNode_NestedRunKeepsParentControlsPrivate(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: builds and runs the process-per-node fixture")
	}
	mod, bin := buildProcPerNodeBinary(t)
	hostBin := wingdHostBin(t)

	home := t.TempDir()
	stopHomeDaemon(t, home)
	probe := t.TempDir()
	handle := filepath.Join(t.TempDir(), "parent-run.json")
	nestedHandle := filepath.Join(t.TempDir(), "nested-run.json")
	runEnv := append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_WINGD_BIN="+hostBin,
		"SPARKWING_LOG_FORMAT=json",
		"PROC_PROBE_DIR="+probe,
		"SPARKWING_RUN_HANDLE_FILE="+handle,
		"NESTED_RUN_HANDLE_FILE="+nestedHandle,
	)

	runBin(t, mod, runEnv, bin, "nestedparent")
	readPID(t, probe, "nested-child")

	body, err := os.ReadFile(handle)
	if err != nil {
		t.Fatalf("read parent run handle: %v", err)
	}
	var got struct {
		Pipeline string `json:"pipeline"`
		RunID    string `json:"run_id"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode parent run handle: %v", err)
	}
	if got.Pipeline != "nestedparent" {
		t.Fatalf("run handle pipeline = %q, want the outer run", got.Pipeline)
	}
	parentRunID := got.RunID
	body, err = os.ReadFile(nestedHandle)
	if err != nil {
		t.Fatalf("read nested run handle: %v", err)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode nested run handle: %v", err)
	}
	if got.Pipeline != "nestedchild" {
		t.Fatalf("nested run handle pipeline = %q, want the explicit nested run", got.Pipeline)
	}

	if got.RunID == parentRunID {
		t.Fatal("nested handle reused the parent run id")
	}
	nestedRunID := got.RunID
	st, err := store.Open(orchestrator.PathsAt(home).StateDB())
	if err != nil {
		t.Fatalf("open run store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close run store: %v", err)
		}
	})
	for _, runID := range []string{parentRunID, nestedRunID} {
		run, err := st.GetRun(context.Background(), runID)
		if err != nil || run == nil || run.Status != "success" {
			t.Fatalf("run %s = %+v, %v; want success", runID, run, err)
		}
	}

	for _, testCase := range []struct {
		name                string
		nestedStartAt       string
		wantPrepareExecuted bool
	}{
		{name: "implicit child runs its own range", wantPrepareExecuted: true},
		{name: "explicit child range is preserved", nestedStartAt: "selected"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			stopHomeDaemon(t, home)
			probe := t.TempDir()
			env := append(os.Environ(),
				"SPARKWING_HOME="+home,
				"SPARKWING_WINGD_BIN="+hostBin,
				"SPARKWING_LOG_FORMAT=json",
				"PROC_PROBE_DIR="+probe,
				"SPARKWING_START_AT=launch-child",
				"SPARKWING_STOP_AT=launch-child",
			)
			if testCase.nestedStartAt != "" {
				env = append(env, "NESTED_START_AT="+testCase.nestedStartAt)
			}

			runBin(t, mod, env, bin, "nestedparent")
			readPID(t, probe, "nested-child")
			_, prepareErr := os.Stat(filepath.Join(probe, "nested-child-prepare.pid"))
			if testCase.wantPrepareExecuted && prepareErr != nil {
				t.Fatalf("implicit child skipped its prepare step: %v", prepareErr)
			}
			if !testCase.wantPrepareExecuted && !os.IsNotExist(prepareErr) {
				t.Fatalf("explicit child selection ran prepare; stat error = %v", prepareErr)
			}
		})
	}
}

func TestProcessPerNode_NodeAbandonsARunWhoseDispatcherDied(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 5.7s of real work; the fast class runs under -short")
	}
	mod, bin := buildProcPerNodeBinary(t)

	home := t.TempDir()
	stopHomeDaemon(t, home)
	probe := t.TempDir()

	cmd := exec.Command(bin, "orphanproof")
	cmd.Dir = mod
	cmd.Env = append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_WINGD_BIN="+wingdHostBin(t),
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
		if processAlive(nodePID) {
			_ = killProcessForTest(nodePID)
		}
	})

	if err := cmd.Process.Kill(); err != nil {
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

type processPerNodeFixturePaths struct {
	onDisk string
	mod    string
	bin    string
	host   string
}

// safety: only generated source and executable bytes are shared. Every test
// still supplies its own home, state backend, profiles, probes, and run IDs.
var processPerNodeFixtureOnce sync.Once

var processPerNodeFixture processPerNodeFixturePaths

func sharedProcessPerNodeFixture(t *testing.T) processPerNodeFixturePaths {
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
	processPerNodeFixtureOnce.Do(func() {
		repoRoot := repoRootDir(t)
		onDisk, err := os.MkdirTemp("", "sparkwing-process-per-node-")
		if err != nil {
			t.Fatalf("create process-per-node fixture root: %v", err)
		}
		processPerNodeFixture.onDisk = onDisk
		processPerNodeFixture.mod = filepath.Join(onDisk, "pipeline")
		if err := os.MkdirAll(processPerNodeFixture.mod, 0o755); err != nil {
			t.Fatalf("create process-per-node module: %v", err)
		}
		writeMod(t, filepath.Join(processPerNodeFixture.mod, "go.mod"), ""+
			"module procpernode\n\ngo 1.26.0\n\n"+
			"require github.com/sparkwing-dev/sparkwing v0.0.0\n\n"+
			"replace github.com/sparkwing-dev/sparkwing => "+repoRoot+"\n")
		writeMod(t, filepath.Join(processPerNodeFixture.mod, "jobs", "jobs.go"), procPerNodeJobs)
		writeMod(t, filepath.Join(processPerNodeFixture.mod, "main.go"), procPerNodeMain)

		buildEnv := append(os.Environ(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
		runGo(t, processPerNodeFixture.mod, buildEnv, "mod", "tidy")
		processPerNodeFixture.bin = filepath.Join(onDisk, "procpernode")
		runGo(t, processPerNodeFixture.mod, buildEnv, "build", "-o", processPerNodeFixture.bin, ".")

		processPerNodeFixture.host = filepath.Join(onDisk, "sparkwing")
		hostEnv := append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
		runGo(t, repoRoot, hostEnv, "build", "-o", processPerNodeFixture.host, "./cmd/sparkwing")
	})
	return processPerNodeFixture
}

func cleanupProcessPerNodeFixture() error {
	if processPerNodeFixture.onDisk == "" {
		return nil
	}
	return os.RemoveAll(processPerNodeFixture.onDisk)
}

func buildProcPerNodeBinary(t *testing.T) (mod, bin string) {
	t.Helper()
	fixture := sharedProcessPerNodeFixture(t)
	return fixture.mod, fixture.bin
}

func wingdHostBin(t *testing.T) string {
	t.Helper()
	return sharedProcessPerNodeFixture(t).host
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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// StampRunID records the run's id where a test can read it without
// opening the run's store while the run still owns it.
func StampRunID(id string) {
	dir := os.Getenv("PROC_PROBE_DIR")
	if dir == "" || id == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "run.id"), []byte(id), 0o644)
}

// RecordAttempt appends this process's pid to the shared attempts file
// and reports how many attempts of the node have now started. The file
// is the only state that survives a bounce, since each attempt is a
// different process.
func RecordAttempt() int {
	path := filepath.Join(os.Getenv("PROC_PROBE_DIR"), "bounce-attempts")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	raw, _ := os.ReadFile(path)
	return len(strings.Fields(string(raw)))
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

type NestedChildJob struct{ sparkwing.Base }

func (j *NestedChildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	prepare := sparkwing.Step(w, "prepare", func(context.Context) error {
		StampPID("nested-child-prepare")
		return nil
	})
	selected := sparkwing.Step(w, "selected", func(context.Context) error {
		StampPID("nested-child")
		return nil
	}).Needs(prepare)
	return selected, nil
}

type Nestedchild struct{ sparkwing.Base }

func (p *Nestedchild) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "inner", &NestedChildJob{})
	return nil
}

func nestedRunEnv(handle string) []string {
	startAt := os.Getenv("NESTED_START_AT")
	env := make([]string, 0, len(os.Environ())+3)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "SPARKWING_RUN_HANDLE_FILE=") {
			continue
		}
		if startAt != "" && (strings.HasPrefix(item, "SPARKWING_START_AT=") ||
			strings.HasPrefix(item, "SPARKWING_STOP_AT=")) {
			continue
		}
		env = append(env, item)
	}
	if handle != "" {
		env = append(env, "SPARKWING_RUN_HANDLE_FILE="+handle)
	}
	if startAt != "" {
		env = append(env, "SPARKWING_START_AT="+startAt, "SPARKWING_STOP_AT="+startAt)
	}
	return env
}

type NestedParentJob struct{ sparkwing.Base }

func (j *NestedParentJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "launch-child", func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, os.Args[0], "nestedchild")
		cmd.Env = nestedRunEnv("")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("nested run: %w: %s", err, output)
		}
		cmd = exec.CommandContext(ctx, os.Args[0], "nestedchild")
		cmd.Env = nestedRunEnv(os.Getenv("NESTED_RUN_HANDLE_FILE"))
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("explicit nested run: %w: %s", err, output)
		}
		return nil
	}), nil
}

type Nestedparent struct{ sparkwing.Base }

func (p *Nestedparent) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "outer", &NestedParentJob{})
	return nil
}

type BounceOut struct {
	Attempt int ` + "`json:\"attempt\"`" + `
	PID     int ` + "`json:\"pid\"`" + `
}

type Bouncer struct {
	sparkwing.Base
	sparkwing.Produces[BounceOut]
}

func selfCPUNanos() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return ru.Utime.Nano() + ru.Stime.Nano()
}

func (j *Bouncer) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (BounceOut, error) {
		attempt := RecordAttempt()
		if attempt == 1 {
			// Burn CPU the exit accounting can see, then wait to be
			// killed. Deliberately ignores ctx: a bounce is a kill, not
			// a cancellation the body can cooperate with.
			// safety: the check downstream reads CPU time, so burn CPU rather than wall
			// time. A loaded machine deschedules this loop, and a spin bounded by the
			// clock then accrues whatever share it was given rather than the amount the
			// check is written against.
			spin := 0
			started := selfCPUNanos()
			wallStop := time.Now().Add(60 * time.Second)
			for time.Now().Before(wallStop) {
				spin++
				if spin%8192 == 0 && selfCPUNanos()-started >= int64(1500*time.Millisecond) {
					break
				}
			}
			_ = spin
			time.Sleep(10 * time.Minute)
		}
		return BounceOut{Attempt: attempt, PID: os.Getpid()}, nil
	}), nil
}

type BounceConsumer struct {
	sparkwing.Base
	From sparkwing.Ref[BounceOut]
}

func (j *BounceConsumer) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "check", func(ctx context.Context) error {
		got := j.From.Get(ctx)
		if got.Attempt != 2 {
			return fmt.Errorf("consumed attempt=%d, want the second (surviving) attempt", got.Attempt)
		}
		sparkwing.Info(ctx, "consumed attempt=%d pid=%d", got.Attempt, got.PID)
		return nil
	}), nil
}

type Bounceproof struct{ sparkwing.Base }

func (p *Bounceproof) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	StampRunID(rc.RunID)
	work := sparkwing.Job(plan, "work", &Bouncer{})
	sparkwing.Job(plan, "after", &BounceConsumer{From: sparkwing.RefTo[BounceOut](work)}).Needs(work)
	return nil
}

func init() {
	sparkwing.Register("spawnproof", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Spawnproof{} })
	sparkwing.Register("orphanproof", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Orphanproof{} })
	sparkwing.Register("spawnnode", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Spawnnode{} })
	sparkwing.Register("bounceproof", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Bounceproof{} })
	sparkwing.Register("nestedchild", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Nestedchild{} })
	sparkwing.Register("nestedparent", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Nestedparent{} })
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

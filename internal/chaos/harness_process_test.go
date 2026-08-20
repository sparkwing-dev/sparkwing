package chaos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const actorHelperMode = "SPARKWING_CHAOS_ACTOR_HELPER"

func TestWatchActorHelperProcess(t *testing.T) {
	switch os.Getenv(actorHelperMode) {
	case "descendant":
		ignoreProcessGroupTermination()
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "actor":
		children, err := strconv.Atoi(os.Getenv("SPARKWING_CHAOS_CHILDREN"))
		if err != nil || children < 1 {
			children = 1
		}
		for range children {
			child := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
			child.Env = append(os.Environ(), actorHelperMode+"=descendant")
			child.Stdout = os.Stdout
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
		}
		time.Sleep(250 * time.Millisecond)
		fmt.Println("OK sentinel-immediately-before-exit")
		os.Exit(0)
	case "daemon":
		child := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
		child.Env = append(os.Environ(), actorHelperMode+"=descendant")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		time.Sleep(250 * time.Millisecond)
		os.Exit(0)
	case "hang":
		ignoreProcessGroupTermination()
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "zombie-parent":
		child := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
		child.Env = append(os.Environ(), actorHelperMode+"=exit")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "exit":
		os.Exit(0)
	}
}

func TestWatchActorReapsExitedProcessAndRecordsFinalOutput(t *testing.T) {
	requireProcessGroups(t)
	h, journal := newProcessHarness(t)
	defer journal.Close()

	cmd := helperCommand("actor", 1)
	stdout, group, err := startActorCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	a := &actor{
		runID:   "exited-parent",
		cmd:     cmd,
		group:   group,
		stdout:  stdout,
		scanned: make(chan struct{}),
	}
	t.Cleanup(func() { _ = h.finishActor(a, true) })
	go h.watchActor(a, stdout)

	deadlineAt := time.Now().Add(2 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
	for {
		if !time.Now().Before(deadlineAt) {
			break
		}
		h.mu.Lock()
		exited, granted := a.exited, a.granted
		h.mu.Unlock()
		if exited {
			if !granted {
				t.Fatal("final protocol output was lost before the actor was reaped")
			}
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
				t.Fatal("actor was marked exited before its process was reaped")
			}
			if !group.Reaped() {
				t.Fatal("actor ownership was released before group cleanup completed")
			}
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			break
		}
	}
	t.Fatal("exited actor was not reaped while its descendant held stdout open")
}

func TestWatchActorBoundsRepeatedIgnoreTermDescendantChurn(t *testing.T) {
	requireProcessGroups(t)
	h, journal := newProcessHarness(t)
	defer journal.Close()
	const actorCount = 20
	var wg sync.WaitGroup
	actors := make([]*actor, 0, actorCount)
	for i := range actorCount {
		cmd := helperCommand("actor", 3)
		stdout, group, err := startActorCommand(cmd)
		if err != nil {
			t.Fatal(err)
		}
		a := &actor{
			runID:   fmt.Sprintf("churn-%d", i),
			cmd:     cmd,
			group:   group,
			stdout:  stdout,
			scanned: make(chan struct{}),
		}
		actors = append(actors, a)
		t.Cleanup(func() { _ = h.finishActor(a, true) })
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.watchActor(a, stdout)
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("repeated actor cleanup exceeded its bound")
	}
	for _, a := range actors {
		if !a.group.Reaped() {
			t.Fatalf("process group %d retained ownership after churn", a.group.ID())
		}
	}
}

func TestManagedDaemonBoundsRepeatedIgnoreTermDescendantChurn(t *testing.T) {
	requireProcessGroups(t)
	h := &Harness{cfg: Config{Settle: time.Second}, t: t, daemons: map[int]*daemonProcess{}}
	const daemonCount = 20
	daemons := make([]*daemonProcess, 0, daemonCount)
	for range daemonCount {
		cmd := helperCommand("daemon", 0)
		if err := h.startDaemonCommand(cmd); err != nil {
			t.Fatal(err)
		}
		h.mu.Lock()
		daemon := h.daemons[cmd.Process.Pid]
		h.mu.Unlock()
		daemons = append(daemons, daemon)
		t.Cleanup(func() { _ = h.finishDaemon(daemon, true) })
	}

	deadlineAt := time.Now().Add(4 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
waitForDaemons:
	for {
		if !time.Now().Before(deadlineAt) {
			break
		}
		h.mu.Lock()
		remaining := len(h.daemons)
		h.mu.Unlock()
		if remaining == 0 {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			break waitForDaemons
		}
	}
	h.mu.Lock()
	remaining := len(h.daemons)
	h.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d managed daemon groups remained after cleanup bound", remaining)
	}
	for _, daemon := range daemons {
		if !daemon.group.Reaped() {
			t.Fatalf("daemon process group %d retained ownership after churn", daemon.group.ID())
		}
	}
}

func TestReadStateCancelsAStalledQueueExchange(t *testing.T) {
	h := &Harness{cfg: Config{OracleTimeout: 25 * time.Millisecond}}
	h.stateReader = func(ctx context.Context) (wingwire.QueueState, error) {
		<-ctx.Done()
		return wingwire.QueueState{}, ctx.Err()
	}
	started := time.Now()
	_, err := h.readState()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readState error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("stalled queue exchange ran for %s", elapsed)
	}
}

func TestProcessGuardRunsWhileQueueStateIsStalled(t *testing.T) {
	requireProcessGroups(t)
	fired := make(chan struct{})
	group, err := procgroup.Start(helperCommand("hang", 0))
	if err != nil {
		t.Fatal(err)
	}
	h := &Harness{
		cfg:           Config{OracleTimeout: 100 * time.Millisecond, MaxOwnedProcesses: 1},
		guardInterval: 5 * time.Millisecond,
		actors:        map[string]*actor{"guarded": {group: group}},
	}
	h.processReader = func() ([]procgroup.Info, error) {
		return []procgroup.Info{{PID: group.ID(), Group: group.ID()}, {PID: group.ID() + 1, Group: group.ID()}}, nil
	}
	h.processFailure = func([]string) { close(fired) }
	h.stateReader = func(ctx context.Context) (wingwire.QueueState, error) {
		<-ctx.Done()
		return wingwire.QueueState{}, ctx.Err()
	}
	stop := h.startProcessGuard()
	defer func() {
		stop()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = group.Terminate(ctx, 20*time.Millisecond)
	}()
	readDone := make(chan struct{})
	go func() {
		_, _ = h.readState()
		close(readDone)
	}()
	select {
	case <-fired:
	case <-time.After(40 * time.Millisecond):
		t.Fatal("process guard was blocked behind QueueState")
	}
	select {
	case <-readDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("bounded QueueState did not cancel")
	}
}

func TestActorCleanupFailureRetainsOwnershipForRetry(t *testing.T) {
	requireProcessGroups(t)
	h, journal := newProcessHarness(t)
	defer journal.Close()
	cmd := helperCommand("hang", 0)
	stdout, group, err := startActorCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	a := &actor{runID: "cleanup-retry", cmd: cmd, group: group, stdout: stdout, scanned: make(chan struct{})}
	go func() {
		_, _ = io.Copy(io.Discard, stdout)
		close(a.scanned)
	}()
	probeFailure := errors.New("injected descendant probe failure")
	group.SetDescendantProbe(func(int, bool, bool) (bool, error) { return false, probeFailure })
	err = h.cleanupActor(a)
	if !errors.Is(err, procgroup.ErrCleanup) || !errors.Is(err, probeFailure) {
		t.Fatalf("first cleanup error = %v, want the injected cleanup failure", err)
	}
	if !strings.Contains(err.Error(), "did not exit within cleanup bound") {
		t.Fatalf("process-group cleanup failure was not surfaced: %v", err)
	}
	if group.Reaped() {
		t.Fatal("cleanup failure discarded process-group ownership")
	}
	h.mu.Lock()
	retained := a.cleanupFailed
	h.mu.Unlock()
	if !retained {
		t.Fatal("cleanup failure did not mark the actor for a later retry")
	}
	group.SetDescendantProbe(nil)
	h.cfg.Settle = 2 * time.Second
	if err := h.finishActor(a, true); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if !group.Reaped() || !a.exited {
		t.Fatal("retry did not reap the retained actor group")
	}
}

func TestDaemonCleanupFailureRetainsLedgerForRetry(t *testing.T) {
	requireProcessGroups(t)
	h := &Harness{cfg: Config{Settle: time.Second}, t: t, daemons: map[int]*daemonProcess{}}
	group, err := procgroup.Start(helperCommand("hang", 0))
	if err != nil {
		t.Fatal(err)
	}
	daemon := &daemonProcess{group: group, done: make(chan struct{})}
	h.daemons[group.ID()] = daemon
	t.Cleanup(func() {
		group.SetDescendantProbe(nil)
		_ = h.finishDaemon(daemon, true)
	})
	probeFailure := errors.New("injected descendant probe failure")
	group.SetDescendantProbe(func(int, bool, bool) (bool, error) { return false, probeFailure })
	err = h.cleanupDaemon(daemon)
	if !errors.Is(err, procgroup.ErrCleanup) || !errors.Is(err, probeFailure) {
		t.Fatalf("first cleanup error = %v, want the injected cleanup failure", err)
	}
	if group.Reaped() {
		t.Fatal("cleanup failure reaped the daemon ownership anchor")
	}
	if h.daemons[group.ID()] != daemon {
		t.Fatal("cleanup failure deleted the daemon ledger entry")
	}
	if !daemon.cleanupFailed {
		t.Fatal("cleanup failure did not mark the daemon for a later retry")
	}
	group.SetDescendantProbe(nil)
	if err := h.cleanupDaemon(daemon); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if !group.Reaped() {
		t.Fatal("retry did not reap the retained daemon group")
	}
	if _, ok := h.daemons[group.ID()]; ok {
		t.Fatal("successful cleanup retained the daemon ledger entry")
	}
}

func TestActorCleanupSeparatesOutputDrainFromProcessGroupFailure(t *testing.T) {
	requireProcessGroups(t)
	h, journal := newProcessHarness(t)
	defer journal.Close()
	h.cfg.Settle = 200 * time.Millisecond
	cmd := helperCommand("exit", 0)
	stdout, group, err := startActorCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdout.Close() }()
	select {
	case <-group.LeaderExited():
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not exit")
	}
	// safety: scanned is never closed, so the drain phase is the only one that
	// can fail and the assertion below cannot pass for the wrong reason.
	a := &actor{runID: "drain-only", cmd: cmd, group: group, stdout: stdout, scanned: make(chan struct{})}
	err = h.cleanupActor(a)
	if !errors.Is(err, errActorDrain) {
		t.Fatalf("cleanup error = %v, want an output-drain failure", err)
	}
	if errors.Is(err, procgroup.ErrCleanup) {
		t.Fatalf("output-drain timeout was reported as a process-group cleanup failure: %v", err)
	}
	if strings.Contains(err.Error(), "did not exit within cleanup bound") {
		t.Fatalf("output-drain timeout claimed the process group did not exit: %v", err)
	}
	if !group.Reaped() {
		t.Fatal("the process group was not reaped before the drain bound elapsed")
	}
}

func TestProcessGuardAcceptsSoakScaleLeaderAnchors(t *testing.T) {
	requireProcessGroups(t)
	h, journal := soakScaleHarness(t)
	defer journal.Close()
	var reported []string
	h.processFailure = func(violations []string) { reported = append(reported, violations...) }

	actors := make([]*actor, 0, h.cfg.MaxActors)
	for i := range h.cfg.MaxActors {
		actors = append(actors, startGuardedActor(t, h, fmt.Sprintf("anchor-%d", i), "actor", 1))
	}
	for _, a := range actors {
		select {
		case <-a.group.LeaderExited():
		case <-time.After(30 * time.Second):
			t.Fatalf("actor group %d leader did not exit", a.group.ID())
		}
		if a.group.Reaped() {
			t.Fatalf("actor group %d was reaped before the guard sampled it", a.group.ID())
		}
	}

	guard := newProcessGuard(h)
	guard.check()
	if len(reported) > 0 {
		t.Fatalf("guard failed on %d retained ownership anchors: %s", len(actors), strings.Join(reported, "; "))
	}
	if len(guard.since) != len(actors) {
		t.Fatalf("guard tracked %d owned zombies, want %d", len(guard.since), len(actors))
	}
}

// TestProcessGuardAcceptsSoakScaleDescendantZombieBurst pins the burst the
// nightly soak actually produces: one daemon kill makes every live actor fork a
// replacement at once, and the losers are zombies until each actor's wait runs.
func TestProcessGuardAcceptsSoakScaleDescendantZombieBurst(t *testing.T) {
	requireProcessGroups(t)
	h, journal := soakScaleHarness(t)
	defer journal.Close()
	var reported []string
	h.processFailure = func(violations []string) { reported = append(reported, violations...) }

	const burst = 12
	actors := make([]*actor, 0, burst)
	for i := range burst {
		actors = append(actors, startGuardedActor(t, h, fmt.Sprintf("burst-%d", i), "zombie-parent", 0))
	}
	guard := newProcessGuard(h)
	deadlineAt := time.Now().Add(30 * time.Second)
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
waitForBurst:
	for {
		if !time.Now().Before(deadlineAt) {
			break
		}
		guard.check()
		if len(guard.since) == len(actors) {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			break waitForBurst
		}
	}
	if len(guard.since) != len(actors) {
		t.Fatalf("guard saw %d descendant zombies, want %d", len(guard.since), len(actors))
	}
	if len(reported) > 0 {
		t.Fatalf("guard failed on a %d-way descendant zombie burst: %s", burst, strings.Join(reported, "; "))
	}
}

func TestProcessGuardFailsWhenADescendantZombieNeverDrains(t *testing.T) {
	requireProcessGroups(t)
	h, journal := soakScaleHarness(t)
	defer journal.Close()
	h.cfg.MaxZombieDrain = 100 * time.Millisecond
	fired := make(chan []string, 1)
	h.processFailure = func(violations []string) { fired <- violations }

	a := startGuardedActor(t, h, "stalled-descendant", "zombie-parent", 0)
	got := awaitGuardViolation(t, h, 30*time.Second, fired)
	want := fmt.Sprintf("in owned group %d stayed unreaped", a.group.ID())
	if !strings.Contains(got, want) || !strings.Contains(got, "descendant pid ") {
		t.Fatalf("guard reported %q, want a stalled descendant in group %d", got, a.group.ID())
	}
}

func TestProcessGuardFailsWhenALeaderAnchorNeverDrains(t *testing.T) {
	requireProcessGroups(t)
	h, journal := soakScaleHarness(t)
	defer journal.Close()
	h.cfg.MaxZombieDrain = 100 * time.Millisecond
	fired := make(chan []string, 1)
	h.processFailure = func(violations []string) { fired <- violations }

	a := startGuardedActor(t, h, "stalled-anchor", "actor", 1)
	select {
	case <-a.group.LeaderExited():
	case <-time.After(30 * time.Second):
		t.Fatal("actor leader did not exit")
	}
	got := awaitGuardViolation(t, h, 10*time.Second, fired)
	want := fmt.Sprintf("owned group %d leader anchor", a.group.ID())
	if !strings.Contains(got, want) || !strings.Contains(got, "stayed unreaped") {
		t.Fatalf("guard reported %q, want a stalled anchor for group %d", got, a.group.ID())
	}
}

func TestProcessGuardExemptsZombiesRetainedByAReportedCleanupFailure(t *testing.T) {
	requireProcessGroups(t)
	h, journal := soakScaleHarness(t)
	defer journal.Close()
	h.cfg.MaxZombieDrain = 50 * time.Millisecond
	var reported []string
	h.processFailure = func(violations []string) { reported = append(reported, violations...) }

	a := startGuardedActor(t, h, "already-reported", "actor", 1)
	select {
	case <-a.group.LeaderExited():
	case <-time.After(30 * time.Second):
		t.Fatal("actor leader did not exit")
	}
	h.mu.Lock()
	a.cleanupFailed = true
	h.mu.Unlock()

	guard := newProcessGuard(h)
	deadlineAt := time.Now().Add(time.Second)
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
observeGuard:
	for {
		if !time.Now().Before(deadlineAt) {
			break
		}
		guard.check()
		select {
		case <-poll.C:
		case <-deadline.C:
			break observeGuard
		}
	}
	if len(reported) > 0 {
		t.Fatalf("guard re-reported an already-reported cleanup failure: %s", strings.Join(reported, "; "))
	}
}

func awaitGuardViolation(t *testing.T, h *Harness, bound time.Duration, fired <-chan []string) string {
	t.Helper()
	guard := newProcessGuard(h)
	deadlineAt := time.Now().Add(bound)
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
	for {
		if !time.Now().Before(deadlineAt) {
			break
		}
		guard.check()
		select {
		case violations := <-fired:
			return strings.Join(violations, "; ")
		default:
		}
		select {
		case violations := <-fired:
			return strings.Join(violations, "; ")
		case <-poll.C:
		case <-deadline.C:
			break
		}
	}
	t.Fatal("process guard reported no violation within its bound")
	return ""
}

func helperCommand(mode string, children int) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
	cmd.Env = append(os.Environ(), actorHelperMode+"="+mode)
	if children > 0 {
		cmd.Env = append(cmd.Env, "SPARKWING_CHAOS_CHILDREN="+strconv.Itoa(children))
	}
	return cmd
}

func newProcessHarness(t *testing.T) (*Harness, *Journal) {
	journal, err := NewJournal(filepath.Join(t.TempDir(), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return &Harness{cfg: Config{Settle: time.Second}, t: t, jr: journal}, journal
}

// soakScaleHarness configures the harness exactly as the nightly soak does, so
// a guard regression that only appears at that actor count is caught here
// rather than by a 30-minute run.
func soakScaleHarness(t *testing.T) (*Harness, *Journal) {
	t.Helper()
	journal, err := NewJournal(filepath.Join(t.TempDir(), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h := &Harness{
		cfg:     SoakConfig(20260801, 30*time.Minute),
		t:       t,
		jr:      journal,
		actors:  map[string]*actor{},
		daemons: map[int]*daemonProcess{},
	}
	return h, journal
}

func startGuardedActor(t *testing.T, h *Harness, runID, mode string, children int) *actor {
	t.Helper()
	cmd := helperCommand(mode, children)
	stdout, group, err := startActorCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	a := &actor{runID: runID, cmd: cmd, group: group, stdout: stdout, scanned: make(chan struct{})}
	go func() {
		_, _ = io.Copy(io.Discard, stdout)
		close(a.scanned)
	}()
	h.mu.Lock()
	h.actors[runID] = a
	h.mu.Unlock()
	t.Cleanup(func() {
		h.mu.Lock()
		a.cleanupFailed = false
		h.mu.Unlock()
		if err := h.finishActor(a, true); err != nil {
			t.Errorf("cleanup actor %s: %v", runID, err)
		}
	})
	return a
}

func requireProcessGroups(t *testing.T) {
	t.Helper()
	if err := procgroup.Supported(); err != nil {
		t.Skip(err)
	}
}

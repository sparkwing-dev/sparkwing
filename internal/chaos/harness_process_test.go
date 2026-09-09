package chaos

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const (
	actorHelperMode    = "SPARKWING_CHAOS_ACTOR_HELPER"
	actorHelperReadyFD = "SPARKWING_CHAOS_ACTOR_READY_FD"
)

func TestWatchActorHelperProcess(t *testing.T) {
	switch os.Getenv(actorHelperMode) {
	case "descendant":
		ignoreProcessGroupTermination()
		descriptor, err := strconv.Atoi(os.Getenv(actorHelperReadyFD))
		if err != nil || descriptor < 3 {
			exitActorHelper(errors.Join(fmt.Errorf("invalid readiness descriptor %q", os.Getenv(actorHelperReadyFD)), err))
		}
		ready := os.NewFile(uintptr(descriptor), "actor-helper-ready")
		_, writeErr := fmt.Fprintln(ready, "ready")
		if err := errors.Join(writeErr, ready.Close()); err != nil {
			exitActorHelper(err)
		}
		blockActorHelper()
		os.Exit(0)
	case "actor":
		children, err := strconv.Atoi(os.Getenv("SPARKWING_CHAOS_CHILDREN"))
		if err != nil || children < 1 {
			exitActorHelper(errors.Join(fmt.Errorf("invalid actor child count %q", os.Getenv("SPARKWING_CHAOS_CHILDREN")), err))
		}
		for range children {
			if err := startReadyDescendant(true); err != nil {
				exitActorHelper(err)
			}
		}
		if _, err := fmt.Println("OK sentinel-immediately-before-exit"); err != nil {
			exitActorHelper(err)
		}
		os.Exit(0)
	case "daemon":
		if err := startReadyDescendant(false); err != nil {
			exitActorHelper(err)
		}
		os.Exit(0)
	case "hang":
		ignoreProcessGroupTermination()
		blockActorHelper()
		os.Exit(0)
	case "zombie-parent":
		child := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
		child.Env = append(os.Environ(), actorHelperMode+"=exit")
		if err := child.Start(); err != nil {
			exitActorHelper(err)
		}
		if err := waitForZombie(child.Process.Pid); err != nil {
			exitActorHelper(err)
		}
		blockActorHelper()
		os.Exit(0)
	case "exit":
		os.Exit(0)
	}
}

func exitActorHelper(err error) {
	if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
		os.Exit(3)
	}
	os.Exit(2)
}

func startReadyDescendant(inheritStdout bool) (startErr error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { startErr = errors.Join(startErr, reader.Close()) }()
	child := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
	child.Env = append(os.Environ(), actorHelperMode+"=descendant", actorHelperReadyFD+"=3")
	child.ExtraFiles = []*os.File{writer}
	if inheritStdout {
		child.Stdout = os.Stdout
	}
	if err := child.Start(); err != nil {
		return errors.Join(err, writer.Close())
	}
	readinessErr := writer.Close()
	if readinessErr == nil {
		type readinessResult struct {
			line string
			err  error
		}
		ready := make(chan readinessResult, 1)
		go func() {
			line, err := bufio.NewReader(reader).ReadString('\n')
			ready <- readinessResult{line: line, err: err}
		}()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case readiness := <-ready:
			if readiness.err == nil && readiness.line == "ready\n" {
				return nil
			}
			readinessErr = errors.Join(fmt.Errorf("invalid descendant readiness %q", readiness.line), readiness.err)
		case <-timer.C:
			readinessErr = errors.New("descendant readiness timed out")
		}
	}
	killErr := child.Process.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	return errors.Join(readinessErr, killErr, child.Wait())
}

func waitForZombie(pid int) error {
	var inspectionErr error
	found := pollProcessState(3*time.Second, 10*time.Millisecond, func() bool {
		processes, err := procgroup.List()
		if err != nil {
			inspectionErr = err
			return true
		}
		for _, process := range processes {
			if process.PID == pid {
				return strings.HasPrefix(process.State, "Z")
			}
		}
		return false
	})
	if inspectionErr != nil {
		return inspectionErr
	}
	if !found {
		return fmt.Errorf("process %d did not become a zombie", pid)
	}
	return nil
}

func blockActorHelper() {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		exitActorHelper(err)
	}
	connection, acceptErr := listener.Accept()
	var connectionCloseErr error
	if connection != nil {
		connectionCloseErr = connection.Close()
	}
	if err := errors.Join(acceptErr, connectionCloseErr, listener.Close()); err != nil {
		exitActorHelper(err)
	}
}

func TestWatchActorReapsExitedProcessAndRecordsFinalOutput(t *testing.T) {
	requireProcessGroups(t)
	harness, journal := newProcessHarness(t)
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close process journal: %v", err)
		}
	})

	command := helperCommand("actor", 1)
	stdout, group, err := startActorCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	watchedActor := &actor{
		runID:   "exited-parent",
		cmd:     command,
		group:   group,
		stdout:  stdout,
		scanned: make(chan struct{}),
	}
	t.Cleanup(func() {
		if err := harness.finishActor(watchedActor, true); err != nil {
			t.Errorf("finish actor: %v", err)
		}
	})
	go harness.watchActor(watchedActor, stdout)

	if pollProcessState(2*time.Second, 10*time.Millisecond, func() bool {
		harness.mu.Lock()
		exited, granted := watchedActor.exited, watchedActor.granted
		harness.mu.Unlock()
		if !exited {
			return false
		}
		if !granted {
			t.Fatal("final protocol output was lost before the actor was reaped")
		}
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			t.Fatal("actor was marked exited before its process was reaped")
		}
		if !group.Reaped() {
			t.Fatal("actor ownership was released before group cleanup completed")
		}
		return true
	}) {
		return
	}
	t.Fatal("exited actor was not reaped while its descendant held stdout open")
}

func TestWatchActorBoundsRepeatedIgnoreTermDescendantChurn(t *testing.T) {
	requireProcessGroups(t)
	harness, journal := newProcessHarness(t)
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close process journal: %v", err)
		}
	})
	const actorCount = 20
	var watchers sync.WaitGroup
	actors := make([]*actor, 0, actorCount)
	for i := range actorCount {
		command := helperCommand("actor", 3)
		stdout, group, err := startActorCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		watchedActor := &actor{
			runID:   fmt.Sprintf("churn-%d", i),
			cmd:     command,
			group:   group,
			stdout:  stdout,
			scanned: make(chan struct{}),
		}
		actors = append(actors, watchedActor)
		t.Cleanup(func() {
			if err := harness.finishActor(watchedActor, true); err != nil {
				t.Errorf("finish actor: %v", err)
			}
		})
		watchers.Add(1)
		go func() {
			defer watchers.Done()
			harness.watchActor(watchedActor, stdout)
		}()
	}

	done := make(chan struct{})
	go func() {
		watchers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("repeated actor cleanup exceeded its bound")
	}
	for _, watchedActor := range actors {
		if !watchedActor.group.Reaped() {
			t.Fatalf("process group %d retained ownership after churn", watchedActor.group.ID())
		}
	}
}

func TestManagedDaemonBoundsRepeatedIgnoreTermDescendantChurn(t *testing.T) {
	requireProcessGroups(t)
	harness := &Harness{cfg: Config{Settle: time.Second}, t: t, daemons: map[int]*daemonProcess{}}
	const daemonCount = 20
	daemons := make([]*daemonProcess, 0, daemonCount)
	for range daemonCount {
		command := helperCommand("daemon", 0)
		if err := harness.startDaemonCommand(command); err != nil {
			t.Fatal(err)
		}
		harness.mu.Lock()
		daemon := harness.daemons[command.Process.Pid]
		harness.mu.Unlock()
		daemons = append(daemons, daemon)
		t.Cleanup(func() {
			if err := harness.finishDaemon(daemon, true); err != nil {
				t.Errorf("finish daemon: %v", err)
			}
		})
	}

	pollProcessState(4*time.Second, 10*time.Millisecond, func() bool {
		harness.mu.Lock()
		remaining := len(harness.daemons)
		harness.mu.Unlock()
		return remaining == 0
	})
	harness.mu.Lock()
	remaining := len(harness.daemons)
	harness.mu.Unlock()
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
	synctest.Test(t, func(t *testing.T) {
		harness := &Harness{cfg: Config{OracleTimeout: 25 * time.Millisecond}}
		harness.stateReader = func(ctx context.Context) (wingwire.QueueState, error) {
			<-ctx.Done()
			return wingwire.QueueState{}, ctx.Err()
		}
		started := time.Now()
		_, err := harness.readState()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("readState error = %v, want deadline exceeded", err)
		}
		if elapsed := time.Since(started); elapsed != harness.cfg.OracleTimeout {
			t.Fatalf("stalled queue exchange ran for %s, want %s", elapsed, harness.cfg.OracleTimeout)
		}
	})
}

func TestProcessGuardRunsWhileQueueStateIsStalled(t *testing.T) {
	requireProcessGroups(t)
	group, err := procgroup.Start(helperCommand("hang", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := group.Terminate(ctx, 20*time.Millisecond); err != nil {
			var exitErr *exec.ExitError
			if errors.Is(err, procgroup.ErrCleanup) || !group.Reaped() || !errors.As(err, &exitErr) || exitErr.ProcessState.ExitCode() != -1 {
				t.Errorf("terminate guarded process: %v", err)
			}
		}
	})
	harness := &Harness{
		cfg:           Config{OracleTimeout: 100 * time.Millisecond, MaxOwnedProcesses: 1},
		guardInterval: 5 * time.Millisecond,
		actors:        map[string]*actor{"guarded": {group: group}},
	}
	harness.processReader = func() ([]procgroup.Info, error) {
		return []procgroup.Info{{PID: group.ID(), Group: group.ID()}, {PID: group.ID() + 1, Group: group.ID()}}, nil
	}
	guardFired := make(chan struct{})
	harness.processFailure = func([]string) { close(guardFired) }
	readEntered := make(chan struct{})
	readRelease := make(chan struct{})
	releaseRead := sync.OnceFunc(func() { close(readRelease) })
	harness.stateReader = func(ctx context.Context) (wingwire.QueueState, error) {
		close(readEntered)
		<-readRelease
		<-ctx.Done()
		return wingwire.QueueState{}, ctx.Err()
	}
	readDone := make(chan struct{})
	readResult := make(chan error, 1)
	go func() {
		_, err := harness.readState()
		readResult <- err
		close(readDone)
	}()
	t.Cleanup(func() {
		releaseRead()
		select {
		case <-readDone:
		case <-time.After(2 * time.Second):
			t.Error("stalled state read did not stop after release")
		}
	})
	deadline, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-readEntered:
	case <-deadline.Done():
		t.Fatal("state reader did not start")
	}
	stop := harness.startProcessGuard()
	t.Cleanup(func() {
		releaseRead()
		stop()
	})
	select {
	case <-guardFired:
	case <-deadline.Done():
		t.Fatal("process guard was blocked behind QueueState")
	}
	select {
	case <-readDone:
		t.Fatal("state reader returned before its release")
	default:
	}
	releaseRead()
	select {
	case <-readDone:
		if err := <-readResult; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stalled state read error = %v, want deadline exceeded", err)
		}
	case <-deadline.Done():
		t.Fatal("bounded QueueState did not cancel")
	}
}

func TestActorCleanupFailureRetainsOwnershipForRetry(t *testing.T) {
	requireProcessGroups(t)
	harness, journal := newProcessHarness(t)
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close process journal: %v", err)
		}
	})
	command := helperCommand("hang", 0)
	stdout, group, err := startActorCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	watchedActor := &actor{runID: "cleanup-retry", cmd: command, group: group, stdout: stdout, scanned: make(chan struct{})}
	t.Cleanup(func() {
		group.SetDescendantProbe(nil)
		if err := harness.finishActor(watchedActor, true); err != nil {
			t.Errorf("finish retained actor: %v", err)
		}
	})
	go func() {
		if _, err := io.Copy(io.Discard, stdout); err != nil {
			t.Errorf("drain actor output: %v", err)
		}
		close(watchedActor.scanned)
	}()
	probeFailure := errors.New("injected descendant probe failure")
	group.SetDescendantProbe(func(context.Context, int, bool, bool) (bool, error) { return false, probeFailure })
	err = harness.cleanupActor(watchedActor)
	if !errors.Is(err, procgroup.ErrCleanup) || !errors.Is(err, probeFailure) {
		t.Fatalf("first cleanup error = %v, want the injected cleanup failure", err)
	}
	if !strings.Contains(err.Error(), "did not exit within cleanup bound") {
		t.Fatalf("process-group cleanup failure was not surfaced: %v", err)
	}
	if group.Reaped() {
		t.Fatal("cleanup failure discarded process-group ownership")
	}
	harness.mu.Lock()
	retained := watchedActor.cleanupFailed
	harness.mu.Unlock()
	if !retained {
		t.Fatal("cleanup failure did not mark the actor for a later retry")
	}
	group.SetDescendantProbe(nil)
	harness.cfg.Settle = 2 * time.Second
	if err := harness.finishActor(watchedActor, true); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if !group.Reaped() || !watchedActor.exited {
		t.Fatal("retry did not reap the retained actor group")
	}
}

func TestDaemonCleanupFailureRetainsLedgerForRetry(t *testing.T) {
	requireProcessGroups(t)
	harness := &Harness{cfg: Config{Settle: time.Second}, t: t, daemons: map[int]*daemonProcess{}}
	group, err := procgroup.Start(helperCommand("hang", 0))
	if err != nil {
		t.Fatal(err)
	}
	daemon := &daemonProcess{group: group, done: make(chan struct{})}
	harness.daemons[group.ID()] = daemon
	t.Cleanup(func() {
		group.SetDescendantProbe(nil)
		if err := harness.finishDaemon(daemon, true); err != nil {
			t.Errorf("finish daemon: %v", err)
		}
	})
	probeFailure := errors.New("injected descendant probe failure")
	group.SetDescendantProbe(func(context.Context, int, bool, bool) (bool, error) { return false, probeFailure })
	err = harness.cleanupDaemon(daemon)
	if !errors.Is(err, procgroup.ErrCleanup) || !errors.Is(err, probeFailure) {
		t.Fatalf("first cleanup error = %v, want the injected cleanup failure", err)
	}
	if group.Reaped() {
		t.Fatal("cleanup failure reaped the daemon ownership anchor")
	}
	if harness.daemons[group.ID()] != daemon {
		t.Fatal("cleanup failure deleted the daemon ledger entry")
	}
	if !daemon.cleanupFailed {
		t.Fatal("cleanup failure did not mark the daemon for a later retry")
	}
	group.SetDescendantProbe(nil)
	if err := harness.cleanupDaemon(daemon); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if !group.Reaped() {
		t.Fatal("retry did not reap the retained daemon group")
	}
	if _, ok := harness.daemons[group.ID()]; ok {
		t.Fatal("successful cleanup retained the daemon ledger entry")
	}
}

func TestActorCleanupSeparatesOutputDrainFromProcessGroupFailure(t *testing.T) {
	requireProcessGroups(t)
	harness, journal := newProcessHarness(t)
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close process journal: %v", err)
		}
	})
	harness.cfg.Settle = 200 * time.Millisecond
	command := helperCommand("exit", 0)
	stdout, group, err := startActorCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stdout.Close(); err != nil {
			t.Errorf("close actor output: %v", err)
		}
	}()
	select {
	case <-group.LeaderExited():
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not exit")
	}

	watchedActor := &actor{runID: "drain-only", cmd: command, group: group, stdout: stdout, scanned: make(chan struct{})}
	err = harness.cleanupActor(watchedActor)
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
	harness, journal := soakScaleHarness(t)
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close process journal: %v", err)
		}
	})
	var reported []string
	harness.processFailure = func(violations []string) { reported = append(reported, violations...) }

	actors := make([]*actor, 0, harness.cfg.MaxActors)
	for i := range harness.cfg.MaxActors {
		actors = append(actors, startGuardedActor(t, harness, fmt.Sprintf("anchor-%d", i), "actor", 1))
	}
	for _, watchedActor := range actors {
		select {
		case <-watchedActor.group.LeaderExited():
		case <-time.After(30 * time.Second):
			t.Fatalf("actor group %d leader did not exit", watchedActor.group.ID())
		}
		if watchedActor.group.Reaped() {
			t.Fatalf("actor group %d was reaped before the guard sampled it", watchedActor.group.ID())
		}
	}

	guard := newProcessGuard(harness)
	guard.check()
	if len(reported) > 0 {
		t.Fatalf("guard failed on %d retained ownership anchors: %s", len(actors), strings.Join(reported, "; "))
	}
	if len(guard.since) != len(actors) {
		t.Fatalf("guard tracked %d owned zombies, want %d", len(guard.since), len(actors))
	}
}

func TestProcessGuardAcceptsSoakScaleDescendantZombieBurst(t *testing.T) {
	requireProcessGroups(t)
	harness, journal := soakScaleHarness(t)
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close process journal: %v", err)
		}
	})
	var reported []string
	harness.processFailure = func(violations []string) { reported = append(reported, violations...) }

	const burst = 12
	actors := make([]*actor, 0, burst)
	for i := range burst {
		actors = append(actors, startGuardedActor(t, harness, fmt.Sprintf("burst-%d", i), "zombie-parent", 0))
	}
	guard := newProcessGuard(harness)
	pollProcessState(30*time.Second, 20*time.Millisecond, func() bool {
		guard.check()
		return len(guard.since) == len(actors)
	})
	if len(guard.since) != len(actors) {
		t.Fatalf("guard saw %d descendant zombies, want %d", len(guard.since), len(actors))
	}
	if len(reported) > 0 {
		t.Fatalf("guard failed on a %d-way descendant zombie burst: %s", burst, strings.Join(reported, "; "))
	}
}

func TestProcessGuardFailsWhenADescendantZombieNeverDrains(t *testing.T) {
	requireProcessGroups(t)
	harness, journal := soakScaleHarness(t)
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close process journal: %v", err)
		}
	})
	harness.cfg.MaxZombieDrain = 100 * time.Millisecond
	fired := make(chan []string, 1)
	harness.processFailure = func(violations []string) { fired <- violations }

	watchedActor := startGuardedActor(t, harness, "stalled-descendant", "zombie-parent", 0)
	got := awaitGuardViolation(t, harness, 30*time.Second, fired)
	want := fmt.Sprintf("in owned group %d stayed unreaped", watchedActor.group.ID())
	if !strings.Contains(got, want) || !strings.Contains(got, "descendant pid ") {
		t.Fatalf("guard reported %q, want a stalled descendant in group %d", got, watchedActor.group.ID())
	}
}

func TestProcessGuardFailsWhenALeaderAnchorNeverDrains(t *testing.T) {
	requireProcessGroups(t)
	harness, journal := soakScaleHarness(t)
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close process journal: %v", err)
		}
	})
	harness.cfg.MaxZombieDrain = 100 * time.Millisecond
	fired := make(chan []string, 1)
	harness.processFailure = func(violations []string) { fired <- violations }

	watchedActor := startGuardedActor(t, harness, "stalled-anchor", "actor", 1)
	select {
	case <-watchedActor.group.LeaderExited():
	case <-time.After(30 * time.Second):
		t.Fatal("actor leader did not exit")
	}
	got := awaitGuardViolation(t, harness, 10*time.Second, fired)
	want := fmt.Sprintf("owned group %d leader anchor", watchedActor.group.ID())
	if !strings.Contains(got, want) || !strings.Contains(got, "stayed unreaped") {
		t.Fatalf("guard reported %q, want a stalled anchor for group %d", got, watchedActor.group.ID())
	}
}

func TestProcessGuardExemptsZombiesRetainedByAReportedCleanupFailure(t *testing.T) {
	requireProcessGroups(t)
	harness, journal := soakScaleHarness(t)
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close process journal: %v", err)
		}
	})
	harness.cfg.MaxZombieDrain = 50 * time.Millisecond
	var reported []string
	harness.processFailure = func(violations []string) { reported = append(reported, violations...) }

	watchedActor := startGuardedActor(t, harness, "already-reported", "actor", 1)
	select {
	case <-watchedActor.group.LeaderExited():
	case <-time.After(30 * time.Second):
		t.Fatal("actor leader did not exit")
	}
	harness.mu.Lock()
	watchedActor.cleanupFailed = true
	harness.mu.Unlock()

	guard := newProcessGuard(harness)
	pollProcessState(time.Second, 20*time.Millisecond, func() bool {
		guard.check()
		return false
	})
	if len(reported) > 0 {
		t.Fatalf("guard re-reported an already-reported cleanup failure: %s", strings.Join(reported, "; "))
	}
}

func awaitGuardViolation(t *testing.T, harness *Harness, bound time.Duration, fired <-chan []string) string {
	t.Helper()
	guard := newProcessGuard(harness)
	var violation string
	if pollProcessState(bound, 20*time.Millisecond, func() bool {
		guard.check()
		select {
		case violations := <-fired:
			violation = strings.Join(violations, "; ")
			return true
		default:
			return false
		}
	}) {
		return violation
	}
	t.Fatal("process guard reported no violation within its bound")
	return ""
}

func pollProcessState(bound, interval time.Duration, predicate func() bool) bool {
	deadlineAt := time.Now().Add(bound)
	poll := time.NewTicker(interval)
	defer poll.Stop()
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
	for {
		if !time.Now().Before(deadlineAt) {
			return false
		}
		if predicate() {
			return true
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			return false
		}
	}
}

func helperCommand(mode string, children int) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestWatchActorHelperProcess$")
	command.Env = append(os.Environ(), actorHelperMode+"="+mode)
	command.Env = append(command.Env, "SPARKWING_CHAOS_CHILDREN="+strconv.Itoa(children))
	return command
}

func newProcessHarness(t *testing.T) (*Harness, *Journal) {
	journal, err := NewJournal(filepath.Join(t.TempDir(), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return &Harness{cfg: Config{Settle: time.Second}, t: t, jr: journal}, journal
}

func soakScaleHarness(t *testing.T) (*Harness, *Journal) {
	t.Helper()
	journal, err := NewJournal(filepath.Join(t.TempDir(), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	harness := &Harness{
		cfg:     SoakConfig(17, 30*time.Minute),
		t:       t,
		jr:      journal,
		actors:  map[string]*actor{},
		daemons: map[int]*daemonProcess{},
	}
	return harness, journal
}

func startGuardedActor(t *testing.T, harness *Harness, runID, mode string, children int) *actor {
	t.Helper()
	command := helperCommand(mode, children)
	stdout, group, err := startActorCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	watchedActor := &actor{runID: runID, cmd: command, group: group, stdout: stdout, scanned: make(chan struct{})}
	go func() {
		if _, err := io.Copy(io.Discard, stdout); err != nil {
			t.Errorf("drain actor output: %v", err)
		}
		close(watchedActor.scanned)
	}()
	harness.mu.Lock()
	harness.actors[runID] = watchedActor
	harness.mu.Unlock()
	t.Cleanup(func() {
		harness.mu.Lock()
		watchedActor.cleanupFailed = false
		harness.mu.Unlock()
		if err := harness.finishActor(watchedActor, true); err != nil {
			t.Errorf("cleanup actor %s: %v", runID, err)
		}
	})
	return watchedActor
}

func requireProcessGroups(t *testing.T) {
	t.Helper()
	if err := procgroup.Supported(); err != nil {
		t.Skip(err)
	}
}

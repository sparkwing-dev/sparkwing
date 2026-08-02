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

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
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
		time.Sleep(10 * time.Millisecond)
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

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		remaining := len(h.daemons)
		h.mu.Unlock()
		if remaining == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
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

func TestActorCleanupTimeoutRetainsOwnershipForRetry(t *testing.T) {
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
	h.cfg.Settle = time.Nanosecond
	err = h.cleanupActor(a)
	if !errors.Is(err, procgroup.ErrCleanup) {
		t.Fatalf("first cleanup error = %v, want cleanup failure", err)
	}
	if !strings.Contains(err.Error(), "did not exit within cleanup bound") {
		t.Fatalf("cleanup timeout was not surfaced: %v", err)
	}
	if group.Reaped() {
		t.Fatal("cleanup failure discarded process-group ownership")
	}
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
	h := &Harness{cfg: Config{Settle: time.Nanosecond}, t: t, daemons: map[int]*daemonProcess{}}
	group, err := procgroup.Start(helperCommand("hang", 0))
	if err != nil {
		t.Fatal(err)
	}
	daemon := &daemonProcess{group: group, done: make(chan struct{})}
	h.daemons[group.ID()] = daemon
	err = h.cleanupDaemon(daemon)
	if !errors.Is(err, procgroup.ErrCleanup) {
		t.Fatalf("first cleanup error = %v, want cleanup failure", err)
	}
	if group.Reaped() {
		t.Fatal("cleanup failure reaped the daemon ownership anchor")
	}
	if h.daemons[group.ID()] != daemon {
		t.Fatal("cleanup failure deleted the daemon ledger entry")
	}
	h.cfg.Settle = 2 * time.Second
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

func requireProcessGroups(t *testing.T) {
	t.Helper()
	if err := procgroup.Supported(); err != nil {
		t.Skip(err)
	}
}

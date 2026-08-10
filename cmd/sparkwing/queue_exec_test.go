package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const queueExecWait = 10 * time.Second

func init() {
	queueExecGuardCommand = func(command []string) *exec.Cmd {
		args := []string{"-test.run=^TestQueueExecGuardProcess$", "--"}
		return exec.Command(os.Args[0], append(args, command...)...)
	}
}

func TestQueueExecGuardProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator == len(os.Args)-1 {
		return
	}
	os.Exit(exitCodeFor(runQueueExecGuard(os.Args[separator+1:])))
}

func TestQueueExecWaitsInDaemonBeforeStartingCommand(t *testing.T) {
	home := queueHome(t)
	serveQueueDaemon(t, home)

	holderClient, err := wingdclient.EnsureDaemon(context.Background(), wingdclient.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("connect holder: %v", err)
	}
	holder, err := holderClient.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:          "existing-bootstrap",
		SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{
			Name: "bootstrap", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue,
		}},
	}, nil)
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Release() })

	marker := filepath.Join(t.TempDir(), "started")
	ready := filepath.Join(t.TempDir(), "ready.json")
	result := make(chan error, 1)
	submittedAt := time.Now()
	go func() {
		result <- runQueue([]string{
			"exec", "--home", home,
			"--run-id", "waiting-bootstrap",
			"--cores", "0.1",
			"--semaphore", "bootstrap",
			"--ready-file", ready,
			"--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", marker, "23",
		})
	}()

	deadline := time.Now().Add(queueExecWait)
	for {
		qs, queryErr := wingdclient.Query(context.Background(), wingdclient.Options{Home: home})
		if queryErr != nil {
			t.Fatalf("query queue: %v", queryErr)
		}
		if len(qs.Waiters) == 1 && qs.Waiters[0].RunID == "waiting-bootstrap" {
			if elapsed := time.Since(submittedAt); elapsed > 250*time.Millisecond {
				t.Fatalf("queue visibility took %s, want at most 250ms", elapsed)
			}
			waiter := qs.Waiters[0]
			if waiter.Position != 1 {
				t.Errorf("position = %d, want 1", waiter.Position)
			}
			if !containsString(waiter.WaitingOn, "bootstrap") {
				t.Errorf("waiting_on = %v, want bootstrap", waiter.WaitingOn)
			}
			if !strings.Contains(waiter.BlockingReason, "bootstrap") {
				t.Errorf("blocking reason = %q, want bootstrap", waiter.BlockingReason)
			}
			break
		}
		select {
		case runErr := <-result:
			t.Fatalf("queue exec returned before admission: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue exec never became visible: %+v", qs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command started before admission: %v", err)
	}
	waitForFile(t, ready)
	if elapsed := time.Since(submittedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("readiness publication took %s, want at most 250ms", elapsed)
	}
	readyBody, err := os.ReadFile(ready)
	if err != nil {
		t.Fatalf("read readiness: %v", err)
	}
	if !strings.Contains(string(readyBody), `"run_id":"waiting-bootstrap"`) ||
		!strings.Contains(string(readyBody), `"state":"queued"`) {
		t.Fatalf("readiness does not bind the queued participant: %s", readyBody)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	select {
	case runErr := <-result:
		if code := exitCodeFor(runErr); code != 23 {
			t.Fatalf("queue exec exit code = %d, want child status 23: %v", code, runErr)
		}
	case <-time.After(queueExecWait):
		t.Fatal("queue exec did not run after promotion")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("promoted command did not run: %v", err)
	}
}

func TestQueueExecHelperProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
		}
	}
	if separator < 0 || (len(os.Args) != separator+3 && len(os.Args) != separator+4) {
		return
	}
	if err := os.WriteFile(os.Args[separator+1], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(97)
	}
	code, err := strconv.Atoi(os.Args[separator+2])
	if err != nil {
		os.Exit(98)
	}
	if len(os.Args) == separator+4 {
		for {
			if _, statErr := os.Stat(os.Args[separator+3]); statErr == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	os.Exit(code)
}

func TestQueueExecSerializesFreshCommandsAndClearsAdmission(t *testing.T) {
	home := queueHome(t)
	serveQueueDaemon(t, home)
	tmp := t.TempDir()
	firstStarted := filepath.Join(tmp, "first-started")
	firstRelease := filepath.Join(tmp, "first-release")
	secondStarted := filepath.Join(tmp, "second-started")

	first := make(chan error, 1)
	go func() {
		first <- runQueue([]string{
			"exec", "--home", home, "--run-id", "bootstrap-first", "--cores", "0.1",
			"--semaphore", "bootstrap", "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", firstStarted, "17", firstRelease,
		})
	}()
	t.Cleanup(func() { _ = os.WriteFile(firstRelease, nil, 0o600) })
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 1 && qs.Holders[0].RunID == "bootstrap-first"
	})
	waitForFile(t, firstStarted)
	if runtime.GOOS != "windows" {
		body, err := os.ReadFile(firstStarted)
		if err != nil {
			t.Fatalf("read command pid: %v", err)
		}
		pid, err := strconv.Atoi(string(body))
		if err != nil {
			t.Fatalf("parse command pid %q: %v", body, err)
		}
		if _, err := procgroup.CaptureSession(pid); err == nil {
			t.Fatal("command replaced the registered session leader instead of running beneath its stable anchor")
		}
	}

	second := make(chan error, 1)
	secondSubmitted := time.Now()
	go func() {
		second <- runQueue([]string{
			"exec", "--home", home, "--run-id", "bootstrap-second", "--cores", "0.1",
			"--semaphore", "bootstrap", "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", secondStarted, "0",
		})
	}()
	queued := waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Waiters) == 1 && qs.Waiters[0].RunID == "bootstrap-second"
	})
	if elapsed := time.Since(secondSubmitted); elapsed > 250*time.Millisecond {
		t.Fatalf("second command visibility took %s, want at most 250ms", elapsed)
	}
	waiter := queued.Waiters[0]
	if waiter.Position != 1 || !containsString(waiter.WaitingOn, "bootstrap") ||
		!strings.Contains(waiter.BlockingReason, "bootstrap") {
		t.Fatalf("second waiter does not expose its blocking cause: %+v", waiter)
	}
	if _, err := os.Stat(secondStarted); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second command started concurrently: %v", err)
	}

	if err := os.WriteFile(firstRelease, nil, 0o600); err != nil {
		t.Fatalf("release first command: %v", err)
	}
	if err := <-first; exitCodeFor(err) != 17 {
		t.Fatalf("first command exit = %d, want 17: %v", exitCodeFor(err), err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second command: %v", err)
	}
	waitForFile(t, secondStarted)
	cleared := waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 0 && len(qs.Waiters) == 0
	})
	if len(cleared.Holders) != 0 || len(cleared.Waiters) != 0 {
		t.Fatalf("queue did not clear: %+v", cleared)
	}
}

func TestQueueExecCancellationAfterGrantFailsAndReapsCommand(t *testing.T) {
	home := queueHome(t)
	serveQueueDaemon(t, home)
	tmp := t.TempDir()
	started := filepath.Join(tmp, "started")
	neverRelease := filepath.Join(tmp, "never-release")
	result := make(chan error, 1)
	go func() {
		result <- runQueue([]string{
			"exec", "--home", home, "--run-id", "running-bootstrap", "--cores", "0.1",
			"--semaphore", "bootstrap", "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", started, "0", neverRelease,
		})
	}()
	waitForFile(t, started)
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 1 && qs.Holders[0].RunID == "running-bootstrap"
	})

	control, err := wingdclient.EnsureDaemon(context.Background(), wingdclient.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("connect control: %v", err)
	}
	found, err := control.CancelLease(context.Background(), "running-bootstrap")
	_ = control.Close()
	if err != nil || !found {
		t.Fatalf("cancel running command: found=%v err=%v", found, err)
	}
	select {
	case runErr := <-result:
		if runErr == nil {
			t.Fatal("cancelled running command returned success")
		}
	case <-time.After(queueExecWait):
		t.Fatal("cancelled running command did not terminate")
	}
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 0 && len(qs.Waiters) == 0
	})
}

func TestQueueExecRefusesUnsupportedProcessOwnershipBeforeAdmission(t *testing.T) {
	home := queueHome(t)
	serveQueueDaemon(t, home)
	originalSupport := queueExecProcessSupport
	queueExecProcessSupport = func() error { return errors.New("session ownership unavailable") }
	t.Cleanup(func() { queueExecProcessSupport = originalSupport })

	tmp := t.TempDir()
	marker := filepath.Join(tmp, "started")
	ready := filepath.Join(tmp, "ready.json")
	err := runQueue([]string{
		"exec", "--home", home, "--run-id", "unsupported-bootstrap", "--cores", "0.1",
		"--ready-file", ready, "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", marker, "0",
	})
	if err == nil || !strings.Contains(err.Error(), "session ownership unavailable") {
		t.Fatalf("queue exec error = %v, want process-ownership refusal", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsupported command started: %v", statErr)
	}
	if _, statErr := os.Stat(ready); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsupported command published readiness: %v", statErr)
	}
	qs, queryErr := wingdclient.Query(context.Background(), wingdclient.Options{Home: home})
	if queryErr != nil {
		t.Fatalf("query queue: %v", queryErr)
	}
	if len(qs.Holders) != 0 || len(qs.Waiters) != 0 {
		t.Fatalf("unsupported command touched admission: %+v", qs)
	}
}

func TestQueueExecLeaseLossTerminatesBeforePromotingNextCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact process-session ownership is unavailable on Windows")
	}
	home := queueHome(t)
	serveQueueDaemon(t, home)
	lost := make(chan struct{})
	originalWatcher := queueExecWatchGuard
	queueExecWatchGuard = func(*wingdclient.Lease, func(wingwire.Cancel), func()) { <-lost }
	t.Cleanup(func() { queueExecWatchGuard = originalWatcher })

	tmp := t.TempDir()
	started := filepath.Join(tmp, "started")
	release := filepath.Join(tmp, "release")
	result := make(chan error, 1)
	resultDone := make(chan struct{})
	go func() {
		result <- runQueue([]string{
			"exec", "--home", home, "--run-id", "lost-bootstrap", "--cores", "0.1",
			"--semaphore", "bootstrap", "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", started, "0", release,
		})
		close(resultDone)
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		select {
		case <-resultDone:
		case <-time.After(queueExecWait):
		}
	})
	waitForFile(t, started)
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 1 && qs.Holders[0].RunID == "lost-bootstrap"
	})
	close(lost)

	var runErr error
	select {
	case runErr = <-result:
	case <-time.After(time.Second):
		t.Fatal("lease loss did not terminate the command within one second")
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "admission lease") {
		t.Fatalf("lease-loss result = %v, want admission lease failure", runErr)
	}
	body, err := os.ReadFile(started)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", body, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child %d survived lease loss: %v", pid, err)
	}
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 0 && len(qs.Waiters) == 0
	})
}

func TestQueueExecSupervisorDeathRetainsAdmissionUntilCommandSessionEnds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact process-session ownership is unavailable on Windows")
	}
	home := queueHome(t)
	serveQueueDaemon(t, home)
	tmp := t.TempDir()
	ready := filepath.Join(tmp, "ready.json")
	started := filepath.Join(tmp, "started")
	release := filepath.Join(tmp, "release")

	supervisor := exec.Command(os.Args[0], "-test.run=^TestQueueExecSupervisorProcess$", "--", home, ready, started, release)
	if err := supervisor.Start(); err != nil {
		t.Fatalf("start queue-exec supervisor: %v", err)
	}
	supervisorDone := make(chan error, 1)
	go func() { supervisorDone <- supervisor.Wait() }()
	acquireCtx, cancelAcquire := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancelAcquire()
		_ = os.WriteFile(release, nil, 0o600)
		waitForQueueExecProcessExit(t, started)
		if supervisor.Process != nil {
			_ = supervisor.Process.Kill()
		}
		select {
		case <-supervisorDone:
		default:
		}
	})

	waitForFile(t, ready)
	waitForFile(t, started)
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 1 && qs.Holders[0].RunID == "guarded-command"
	})

	followerClient, err := wingdclient.EnsureDaemon(acquireCtx, wingdclient.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("connect follower: %v", err)
	}
	t.Cleanup(func() { _ = followerClient.Close() })
	type acquireResult struct {
		lease *wingdclient.Lease
		err   error
	}
	followerResult := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := followerClient.Acquire(acquireCtx, wingwire.AdmissionRequest{
			RunID: "guarded-follower", SemaphoresOnly: true,
			Semaphores: []wingwire.SemaphoreClaim{{Name: "bootstrap", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue}},
		}, nil)
		followerResult <- acquireResult{lease: lease, err: acquireErr}
	}()
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Waiters) == 1 && qs.Waiters[0].RunID == "guarded-follower"
	})

	if err := supervisor.Process.Kill(); err != nil {
		t.Fatalf("kill queue-exec supervisor: %v", err)
	}
	if err := <-supervisorDone; err == nil {
		t.Fatal("killed queue-exec supervisor exited successfully")
	}
	select {
	case result := <-followerResult:
		if result.lease != nil {
			_ = result.lease.Release()
		}
		t.Fatalf("follower promoted while the killed supervisor's command remained live: %v", result.err)
	case <-time.After(250 * time.Millisecond):
	}
	state := waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 1 && len(qs.Waiters) == 1
	})
	if state.Holders[0].RunID != "guarded-command" || state.Waiters[0].RunID != "guarded-follower" {
		t.Fatalf("supervisor death changed guarded admission: %+v", state)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release guarded command: %v", err)
	}
	select {
	case result := <-followerResult:
		if result.err != nil {
			t.Fatalf("follower admission after guarded command exit: %v", result.err)
		}
		if err := result.lease.Release(); err != nil {
			t.Fatalf("release follower: %v", err)
		}
	case <-time.After(queueExecWait):
		t.Fatal("follower did not promote after the guarded command exited")
	}
}

func TestQueueExecSurvivesAdmissionDaemonRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact process-session ownership is unavailable on Windows")
	}
	home := queueHome(t)
	stopFirst := startRestartableQueueDaemon(t, home)
	tmp := t.TempDir()
	started := filepath.Join(tmp, "started")
	release := filepath.Join(tmp, "release")
	result := make(chan error, 1)
	go func() {
		result <- runQueue([]string{
			"exec", "--home", home, "--run-id", "restart-command", "--cores", "0.1",
			"--semaphore", "bootstrap", "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", started, "0", release,
		})
	}()
	waitForFile(t, started)
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 1 && qs.Holders[0].RunID == "restart-command"
	})
	stopFirst()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), queueExecWait)
		defer cancel()
		if err := wingdclient.Stop(ctx, wingdclient.Options{Home: home}); err != nil && !errors.Is(err, wingdclient.ErrNoDaemon) {
			t.Errorf("stop restarted queue daemon: %v", err)
		}
	})
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 1 && qs.Holders[0].RunID == "restart-command"
	})
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("queue exec after daemon restart: %v", err)
		}
	case <-time.After(queueExecWait):
		t.Fatal("queue exec did not finish after daemon restart")
	}
}

func startRestartableQueueDaemon(t *testing.T, home string) func() {
	t.Helper()
	d, err := wingd.New(wingd.Config{Home: home, Version: "v1.0.0", GuardInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-done:
		t.Fatalf("restartable daemon exited before ready: %v", err)
	case <-time.After(queueExecWait):
		t.Fatal("restartable daemon never became ready")
	}
	var stopped atomic.Bool
	stop := func() {
		if !stopped.CompareAndSwap(false, true) {
			return
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("restartable daemon exit: %v", err)
			}
		case <-time.After(queueExecWait):
			t.Error("restartable daemon did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestQueueExecSupervisorProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
		}
	}
	if separator < 0 || len(os.Args) != separator+5 {
		return
	}
	err := runQueue([]string{
		"exec", "--home", os.Args[separator+1], "--run-id", "guarded-command", "--cores", "0.1",
		"--semaphore", "bootstrap", "--ready-file", os.Args[separator+2],
		"--", os.Args[0], "-test.run=^TestQueueExecHelperProcess$", "--", os.Args[separator+3], "0", os.Args[separator+4],
	})
	os.Exit(exitCodeFor(err))
}

func waitForQueueExecProcessExit(t *testing.T, pidFile string) {
	t.Helper()
	body, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil {
		t.Errorf("parse queue-exec cleanup pid %q: %v", body, err)
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("queue-exec test process %d survived cleanup", pid)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestQueueExecCancellationBeforeGrantNeverStartsCommand(t *testing.T) {
	home := queueHome(t)
	serveQueueDaemon(t, home)
	holderClient, err := wingdclient.EnsureDaemon(context.Background(), wingdclient.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("connect holder: %v", err)
	}
	holder, err := holderClient.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID: "cancel-blocker", SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{Name: "bootstrap", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue}},
	}, nil)
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Release() })

	marker := filepath.Join(t.TempDir(), "cancelled-started")
	result := make(chan error, 1)
	go func() {
		result <- runQueue([]string{
			"exec", "--home", home, "--run-id", "cancelled-bootstrap", "--cores", "0.1",
			"--semaphore", "bootstrap", "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", marker, "0",
		})
	}()
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Waiters) == 1 && qs.Waiters[0].RunID == "cancelled-bootstrap"
	})

	control, err := wingdclient.EnsureDaemon(context.Background(), wingdclient.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("connect control: %v", err)
	}
	cancelledAt := time.Now()
	found, err := control.CancelLease(context.Background(), "cancelled-bootstrap")
	_ = control.Close()
	if err != nil || !found {
		t.Fatalf("cancel queued command: found=%v err=%v", found, err)
	}
	select {
	case runErr := <-result:
		if runErr == nil {
			t.Fatal("cancelled queue exec returned success")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("queued cancellation did not return within 250ms")
	}
	if elapsed := time.Since(cancelledAt); elapsed > 250*time.Millisecond {
		t.Fatalf("queued cancellation took %s, want at most 250ms", elapsed)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled command started: %v", err)
	}
	state := waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool { return len(qs.Waiters) == 0 })
	if len(state.Holders) != 1 || state.Holders[0].RunID != "cancel-blocker" {
		t.Fatalf("cancellation disturbed the holder or leaked admission: %+v", state)
	}
}

func waitForQueueExecState(t *testing.T, home string, ready func(wingwire.QueueState) bool) wingwire.QueueState {
	t.Helper()
	deadline := time.Now().Add(queueExecWait)
	for {
		qs, err := wingdclient.Query(context.Background(), wingdclient.Options{Home: home})
		if err != nil {
			t.Fatalf("query queue: %v", err)
		}
		if ready(qs) {
			return qs
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue state did not converge: %+v", qs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(queueExecWait)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file did not appear: %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

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
	"sync"
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

	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(queueExecWait)
	defer deadline.Stop()
	for {
		qs, queryErr := wingdclient.Query(context.Background(), wingdclient.Options{Home: home, Version: "v1.0.0"})
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
		case <-deadline.C:
			t.Fatalf("queue exec never became visible: %+v", qs)
		case <-poll.C:
		}
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
		poll := time.NewTicker(10 * time.Millisecond)
		deadline := time.NewTimer(queueExecWait)
		for {
			if _, statErr := os.Stat(os.Args[separator+3]); statErr == nil {
				break
			}
			select {
			case <-poll.C:
			case <-deadline.C:
				os.Exit(99)
			}
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
	firstStartedBody := waitForFileContents(t, firstStarted)
	if runtime.GOOS != "windows" {
		pid, err := strconv.Atoi(string(firstStartedBody))
		if err != nil {
			t.Fatalf("parse command pid %q: %v", firstStartedBody, err)
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
	qs, queryErr := wingdclient.Query(context.Background(), wingdclient.Options{Home: home, Version: "v1.0.0"})
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
	queueExecWatchGuard = func(*wingdclient.Lease, func(wingwire.Cancel), func()) error {
		<-lost
		return nil
	}
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
	startedBody := waitForFileContents(t, started)
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
	pid, err := strconv.Atoi(string(startedBody))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", startedBody, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child %d survived lease loss: %v", pid, err)
	}
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 0 && len(qs.Waiters) == 0
	})
}

func TestQueueExecLeaderExitDeclaresCompletionBeforeRejectedReattach(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact process-session ownership is unavailable on Windows")
	}
	home := queueHome(t)
	serveQueueDaemon(t, home)
	completionDeclared := make(chan struct{})
	originalDeclaration := queueExecDeclareGuardComplete
	queueExecDeclareGuardComplete = func(lease *wingdclient.Lease) error {
		err := lease.CompleteGuard()
		close(completionDeclared)
		return err
	}
	t.Cleanup(func() { queueExecDeclareGuardComplete = originalDeclaration })
	originalWatcher := queueExecWatchGuard
	queueExecWatchGuard = func(_ *wingdclient.Lease, _ func(wingwire.Cancel), onComplete func()) error {
		<-completionDeclared
		onComplete()
		return nil
	}
	t.Cleanup(func() { queueExecWatchGuard = originalWatcher })

	tmp := t.TempDir()
	started := filepath.Join(tmp, "started")
	release := filepath.Join(tmp, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	result := make(chan error, 1)
	go func() {
		result <- runQueue([]string{
			"exec", "--home", home, "--run-id", "completed-bootstrap", "--cores", "0.1",
			"--semaphore", "bootstrap", "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", started, "0", release,
		})
	}()
	waitForFile(t, started)
	select {
	case <-completionDeclared:
		t.Fatal("guard completion was declared before the direct leader exited")
	default:
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completionDeclared:
	case <-time.After(queueExecWait):
		t.Fatal("direct leader exit did not declare guard completion")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("queue exec after rejected reattach completion: %v", err)
		}
	case <-time.After(queueExecWait):
		t.Fatal("queue exec did not finish after rejected reattach completion")
	}
}

func TestQueueExecAcceptsRejectedReattachAfterExactSessionIsEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact process-session ownership is unavailable on Windows")
	}
	home := queueHome(t)
	serveQueueDaemon(t, home)
	watched := make(chan *wingdclient.Lease, 1)
	reject := make(chan struct{})
	var rejectOnce sync.Once
	rejectWatch := func() { rejectOnce.Do(func() { close(reject) }) }
	t.Cleanup(rejectWatch)
	originalWatcher := queueExecWatchGuard
	queueExecWatchGuard = func(lease *wingdclient.Lease, _ func(wingwire.Cancel), _ func()) error {
		watched <- lease
		<-reject
		return wingdclient.ErrReattachRejected
	}
	t.Cleanup(func() { queueExecWatchGuard = originalWatcher })
	declarationStarted := make(chan struct{})
	allowDeclaration := make(chan struct{})
	var declarationOnce sync.Once
	var allowOnce sync.Once
	releaseDeclaration := func() { allowOnce.Do(func() { close(allowDeclaration) }) }
	t.Cleanup(releaseDeclaration)
	originalDeclaration := queueExecDeclareGuardComplete
	queueExecDeclareGuardComplete = func(*wingdclient.Lease) error {
		declarationOnce.Do(func() { close(declarationStarted) })
		<-allowDeclaration
		return nil
	}
	t.Cleanup(func() { queueExecDeclareGuardComplete = originalDeclaration })

	tmp := t.TempDir()
	started := filepath.Join(tmp, "started")
	release := filepath.Join(tmp, "release")
	result := make(chan error, 1)
	go func() {
		result <- runQueue([]string{
			"exec", "--home", home, "--run-id", "rejected-empty", "--cores", "0.1",
			"--semaphore", "bootstrap", "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", started, "0", release,
		})
	}()
	waitForFile(t, started)
	lease := <-watched
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-declarationStarted:
	case <-time.After(queueExecWait):
		t.Fatal("leader exit was not observed")
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("disconnect completed guard: %v", err)
	}
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 0
	})
	rejectWatch()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("queue exec after authoritative rejection: %v", err)
		}
	case <-time.After(queueExecWait):
		t.Fatal("queue exec waited for an impossible completion acknowledgement")
	}
	releaseDeclaration()
}

func TestQueueExecRejectsReattachWhileExactSessionIsLive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact process-session ownership is unavailable on Windows")
	}
	home := queueHome(t)
	serveQueueDaemon(t, home)
	watching := make(chan struct{})
	reject := make(chan struct{})
	var watchingOnce sync.Once
	var rejectOnce sync.Once
	rejectWatch := func() { rejectOnce.Do(func() { close(reject) }) }
	t.Cleanup(rejectWatch)
	originalWatcher := queueExecWatchGuard
	queueExecWatchGuard = func(*wingdclient.Lease, func(wingwire.Cancel), func()) error {
		watchingOnce.Do(func() { close(watching) })
		<-reject
		return wingdclient.ErrReattachRejected
	}
	t.Cleanup(func() { queueExecWatchGuard = originalWatcher })

	tmp := t.TempDir()
	started := filepath.Join(tmp, "started")
	neverRelease := filepath.Join(tmp, "never-release")
	t.Cleanup(func() { _ = os.WriteFile(neverRelease, nil, 0o600) })
	result := make(chan error, 1)
	go func() {
		result <- runQueue([]string{
			"exec", "--home", home, "--run-id", "rejected-live", "--cores", "0.1",
			"--semaphore", "bootstrap", "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", started, "0", neverRelease,
		})
	}()
	startedBody := waitForFileContents(t, started)
	select {
	case <-watching:
	case <-time.After(queueExecWait):
		t.Fatal("guard watch did not start")
	}
	rejectWatch()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "admission lease") {
			t.Fatalf("live-session rejection = %v, want lease loss", err)
		}
	case <-time.After(queueExecWait):
		t.Fatal("live-session rejection did not terminate the command")
	}
	pid, err := strconv.Atoi(string(startedBody))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", startedBody, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child %d survived authoritative rejection: %v", pid, err)
	}
	waitForQueueExecState(t, home, func(qs wingwire.QueueState) bool {
		return len(qs.Holders) == 0
	})
}

func TestQueueExecRejectedReattachFailsClosedWhenSessionCannotBeInspected(t *testing.T) {
	gone, err := queueExecGuardAlreadyGone(wingdclient.ErrReattachRejected, procgroup.SessionIdentity{})
	if gone || err == nil {
		t.Fatalf("uninspectable session = gone %v, error %v", gone, err)
	}
}

func TestQueueExecFinishedFirstPreservesWatchFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact process-session ownership is unavailable on Windows")
	}
	home := queueHome(t)
	serveQueueDaemon(t, home)
	releaseStarted := make(chan struct{})
	var releaseOnce sync.Once
	originalRelease := queueExecReleaseGuard
	queueExecReleaseGuard = func(*wingdclient.Lease) error {
		releaseOnce.Do(func() { close(releaseStarted) })
		return nil
	}
	t.Cleanup(func() { queueExecReleaseGuard = originalRelease })
	watchFailure := errors.New("watch recovery failed")
	originalWatcher := queueExecWatchGuard
	queueExecWatchGuard = func(*wingdclient.Lease, func(wingwire.Cancel), func()) error {
		<-releaseStarted
		return watchFailure
	}
	t.Cleanup(func() { queueExecWatchGuard = originalWatcher })

	tmp := t.TempDir()
	started := filepath.Join(tmp, "started")
	release := filepath.Join(tmp, "release")
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := runQueue([]string{
		"exec", "--home", home, "--run-id", "finished-first", "--cores", "0.1",
		"--semaphore", "bootstrap", "--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", started, "0", release,
	})
	if !errors.Is(err, watchFailure) {
		t.Fatalf("queue exec error = %v, want watch failure", err)
	}
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

	supervisor := exec.Command(os.Args[0], "-test.run=^TestQueueExecSupervisorProcess$", "--", home, ready, started, release, "v1.0.0")
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
	successorReady := installQueueExecInProcessSuccessor(t, home)
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
	select {
	case <-successorReady:
	case err := <-result:
		t.Fatalf("queue exec ended before successor readiness: %v", err)
	case <-time.After(2 * queueExecWait):
		t.Fatal("successor daemon did not become ready")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), queueExecWait)
		defer cancel()
		if err := wingdclient.Stop(ctx, wingdclient.Options{Home: home, Version: "v1.0.0"}); err != nil && !errors.Is(err, wingdclient.ErrNoDaemon) {
			t.Errorf("stop restarted queue daemon: %v", err)
		}
	})
	waitForRestartedQueueExecState(t, home, result, func(qs wingwire.QueueState) bool {
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
	case <-time.After(2 * queueExecWait):
		t.Fatal("queue exec did not finish after daemon restart")
	}
}

func TestStartQueueExecSuccessorWaitsForReadiness(t *testing.T) {
	ready := make(chan struct{})
	runStarted := make(chan struct{})
	allowReady := make(chan struct{})
	stopRun := make(chan struct{})
	var allowReadyOnce sync.Once
	var stopRunOnce sync.Once
	releaseReady := func() { allowReadyOnce.Do(func() { close(allowReady) }) }
	releaseRun := func() { stopRunOnce.Do(func() { close(stopRun) }) }
	runFinished := make(chan struct{})
	type startResult struct {
		done <-chan error
		err  error
	}
	returned := make(chan startResult, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		releaseReady()
		releaseRun()
		select {
		case <-runFinished:
		case <-time.After(queueExecWait):
			t.Error("successor run did not stop during cleanup")
		}
		select {
		case <-finished:
		case <-time.After(queueExecWait):
			t.Error("successor helper did not stop during cleanup")
		}
	})
	go func() {
		done, err := startQueueExecSuccessor(func() error {
			defer close(runFinished)
			close(runStarted)
			<-allowReady
			close(ready)
			<-stopRun
			return nil
		}, ready, queueExecWait)
		returned <- startResult{done: done, err: err}
		close(finished)
	}()
	select {
	case <-runStarted:
	case <-time.After(queueExecWait):
		t.Fatal("successor run did not start")
	}
	select {
	case result := <-returned:
		t.Fatalf("successor start returned before readiness: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	releaseReady()
	var result startResult
	select {
	case result = <-returned:
		if result.err != nil {
			t.Fatalf("successor start: %v", result.err)
		}
	case <-time.After(queueExecWait):
		t.Fatal("successor start did not return after readiness")
	}
	releaseRun()
	select {
	case err := <-result.done:
		if err != nil {
			t.Fatalf("successor run: %v", err)
		}
	case <-time.After(queueExecWait):
		t.Fatal("successor run did not stop")
	}
	select {
	case <-finished:
	case <-time.After(queueExecWait):
		t.Fatal("successor helper did not finish")
	}
}

func installQueueExecInProcessSuccessor(t *testing.T, home string) <-chan struct{} {
	t.Helper()
	original := queueExecClientOptions
	var mu sync.Mutex
	ready := make(chan struct{})
	var readyOnce sync.Once
	type daemonInstance struct {
		cancel context.CancelFunc
		done   <-chan error
	}
	var instances []daemonInstance
	queueExecClientOptions = func(gotHome string) wingdclient.Options {
		return wingdclient.Options{
			Home: gotHome, Version: Version, Logf: t.Logf,
			Spawn: func(spawnHome, _ string) error {
				d, err := wingd.New(queueRestartDaemonConfig(spawnHome))
				if err != nil {
					return err
				}
				ctx, stop := context.WithCancel(context.Background())
				done, err := startQueueExecSuccessor(func() error { return d.Run(ctx) }, d.Ready(), queueExecWait)
				if err != nil {
					stop()
					return err
				}
				mu.Lock()
				instances = append(instances, daemonInstance{cancel: stop, done: done})
				mu.Unlock()
				readyOnce.Do(func() { close(ready) })
				return nil
			},
		}
	}
	t.Cleanup(func() {
		queueExecClientOptions = original
		mu.Lock()
		spawned := append([]daemonInstance(nil), instances...)
		mu.Unlock()
		for _, instance := range spawned {
			instance.cancel()
		}
		for _, instance := range spawned {
			select {
			case err := <-instance.done:
				if err != nil && !errors.Is(err, wingd.ErrNotElected) {
					t.Errorf("in-process successor exit: %v", err)
				}
			case <-time.After(queueExecWait):
				t.Error("in-process successor did not stop")
			}
		}
	})
	return ready
}

func startQueueExecSuccessor(run func() error, ready <-chan struct{}, timeout time.Duration) (<-chan error, error) {
	done := make(chan error, 1)
	go func() { done <- run() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ready:
		return done, nil
	case err := <-done:
		if err == nil {
			err = errors.New("successor exited before readiness")
		}
		return done, err
	case <-timer.C:
		return done, errors.New("successor readiness timed out")
	}
}

func waitForRestartedQueueExecState(t *testing.T, home string, result <-chan error, ready func(wingwire.QueueState) bool) wingwire.QueueState {
	t.Helper()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(queueExecWait)
	defer deadline.Stop()
	for {
		select {
		case err := <-result:
			t.Fatalf("queue exec ended during daemon restart: %v", err)
		default:
		}
		qs, err := wingdclient.Query(context.Background(), wingdclient.Options{Home: home, Version: "v1.0.0"})
		if err == nil && ready(qs) {
			return qs
		}
		if err != nil && !errors.Is(err, wingdclient.ErrNoDaemon) {
			t.Fatalf("query restarted queue: %v", err)
		}
		select {
		case err := <-result:
			t.Fatalf("queue exec ended during daemon restart: %v", err)
		case <-deadline.C:
			t.Fatalf("restarted queue did not converge: %+v (last error %v)", qs, err)
		case <-poll.C:
		}
	}
}

func startRestartableQueueDaemon(t *testing.T, home string) func() {
	t.Helper()
	d, err := wingd.New(queueRestartDaemonConfig(home))
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

func queueRestartDaemonConfig(home string) wingd.Config {
	config := queueDaemonConfig(home)
	config.GuardInterval = 10 * time.Millisecond
	return config
}

func TestQueueRestartDaemonConfigUsesDeterministicHostSample(t *testing.T) {
	if queueRestartDaemonConfig(t.TempDir()).Sampler == nil {
		t.Fatal("queue restart test daemon uses the live host sampler")
	}
}

func TestQueueExecSupervisorProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
		}
	}
	if separator < 0 || len(os.Args) != separator+6 {
		return
	}
	Version = os.Args[separator+5]
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
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Errorf("queue-exec test process %d survived cleanup", pid)
			return
		}
	}
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
	case <-time.After(queueExecWait):
		t.Fatal("queued cancellation did not return")
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
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(queueExecWait)
	defer deadline.Stop()
	for {
		qs, err := wingdclient.Query(context.Background(), wingdclient.Options{Home: home, Version: "v1.0.0"})
		if err != nil {
			t.Fatalf("query queue: %v", err)
		}
		if ready(qs) {
			return qs
		}
		select {
		case <-deadline.C:
			t.Fatalf("queue state did not converge: %+v", qs)
		case <-poll.C:
		}
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(queueExecWait)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("file did not appear: %s", path)
		case <-poll.C:
		}
	}
}

func waitForFileContents(t *testing.T, path string) []byte {
	t.Helper()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(queueExecWait)
	defer deadline.Stop()
	for {
		body, err := os.ReadFile(path)
		if err == nil && len(body) > 0 {
			return body
		}
		select {
		case <-deadline.C:
			t.Fatalf("file contents did not appear: %s: %v", path, err)
		case <-poll.C:
		}
	}
}

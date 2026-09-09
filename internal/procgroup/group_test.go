//go:build !windows

package procgroup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const (
	helperMode        = "SPARKWING_PROCGROUP_HELPER"
	procgroupReadyEnv = "SPARKWING_PROCGROUP_READY"
	procgroupReadyFD  = "SPARKWING_PROCGROUP_READY_FD"
	procgroupTermSeen = "SPARKWING_PROCGROUP_TERM_SEEN"
	procgroupOwnedPID = "SPARKWING_PROCGROUP_OWNED_PID"
)

func TestGroupHelperProcess(t *testing.T) {
	switch os.Getenv(helperMode) {
	case "descendant":
		IgnoreTermination()
		holdHelperProcess(os.Getenv(procgroupReadyEnv))
	case "leader":
		if err := startReadyGroupDescendant(false); err != nil {
			exitGroupHelper(err)
		}
		IgnoreTermination()
		os.Exit(0)
	case "short":
		os.Exit(0)
	case "concurrent-cleanup":
		IgnoreTermination()
		holdHelperProcess("")
	case "session-leader":
		if err := startReadyGroupDescendant(true); err != nil {
			exitGroupHelper(err)
		}
		os.Exit(0)
	case "session-stubborn":
		IgnoreTermination()
		child := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
		child.Env = append(os.Environ(), helperMode+"=descendant")
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			exitGroupHelper(err)
		}
		holdHelperProcess("")
	case "session-parked":
		IgnoreTermination()
		holdHelperProcess("")
	case "owner":
		ForwardTerminationToOwned()
		if err := startOwnedSessionDescendant(os.Getenv(procgroupOwnedPID)); err != nil {
			exitGroupHelper(err)
		}
		holdHelperProcess(os.Getenv(procgroupReadyEnv))
	case "session-cooperative":
		term := make(chan os.Signal, 1)
		release := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		signal.Notify(release, syscall.SIGUSR1)
		if err := os.WriteFile(os.Getenv(procgroupReadyEnv), []byte("ready"), 0o600); err != nil {
			exitGroupHelper(err)
		}
		<-term
		if err := os.WriteFile(os.Getenv(procgroupTermSeen), []byte("term"), 0o600); err != nil {
			exitGroupHelper(err)
		}
		<-release
		if err := os.WriteFile(os.Getenv("SPARKWING_PROCGROUP_MARKER"), []byte("clean"), 0o600); err != nil {
			exitGroupHelper(err)
		}
		os.Exit(0)
	}
}

func exitGroupHelper(err error) {
	if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
		os.Exit(3)
	}
	os.Exit(2)
}

func holdHelperProcess(ready string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		exitGroupHelper(err)
	}
	if descriptorText := os.Getenv(procgroupReadyFD); descriptorText != "" {
		descriptor, err := strconv.Atoi(descriptorText)
		if err != nil || descriptor < 3 {
			exitGroupHelper(errors.Join(fmt.Errorf("invalid readiness descriptor %q", descriptorText), err, listener.Close()))
		}
		readyPipe := os.NewFile(uintptr(descriptor), "procgroup-helper-ready")
		_, writeErr := readyPipe.Write([]byte{1})
		if err := errors.Join(writeErr, readyPipe.Close()); err != nil {
			exitGroupHelper(errors.Join(err, listener.Close()))
		}
	}
	if ready != "" {
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			exitGroupHelper(errors.Join(err, listener.Close()))
		}
	}
	connection, acceptErr := listener.Accept()
	var connectionCloseErr error
	if connection != nil {
		connectionCloseErr = connection.Close()
	}
	exitGroupHelper(errors.Join(errors.New("helper listener stopped blocking"), acceptErr, connectionCloseErr, listener.Close()))
}

func startReadyGroupDescendant(setpgid bool) (resultErr error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create descendant readiness pipe: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	child := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	child.Env = append(os.Environ(), helperMode+"=descendant", procgroupReadyFD+"=3")
	child.ExtraFiles = []*os.File{writer}
	if setpgid {
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := child.Start(); err != nil {
		return errors.Join(fmt.Errorf("start descendant: %w", err), writer.Close())
	}
	readyErr := writer.Close()
	if readyErr == nil {
		readyErr = awaitProcgroupReadyByte(reader, 3*time.Second)
	}
	if readyErr == nil {
		return nil
	}
	killErr := child.Process.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	join := time.NewTimer(time.Second)
	defer join.Stop()
	select {
	case waitErr := <-waited:
		return errors.Join(readyErr, killErr, waitErr)
	case <-join.C:
		return errors.Join(readyErr, killErr, errors.New("descendant did not stop after kill"))
	}
}

func awaitProcgroupReadyByte(reader *os.File, timeout time.Duration) error {
	type readyResult struct {
		count int
		value byte
		err   error
	}
	ready := make(chan readyResult, 1)
	go func() {
		buffer := make([]byte, 1)
		count, err := reader.Read(buffer)
		ready <- readyResult{count: count, value: buffer[0], err: err}
	}()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	select {
	case result := <-ready:
		if result.err != nil || result.count != 1 || result.value != 1 {
			return errors.Join(fmt.Errorf("invalid readiness signal: count=%d value=%d", result.count, result.value), result.err)
		}
		return nil
	case <-deadline.C:
		return errors.New("timed out waiting for readiness signal")
	}
}

func TestSessionIdentityBindsInspectionToLeaderBirth(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	command.Env = append(os.Environ(), helperMode+"=session-parked")
	group, err := StartSession(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(t, group) })
	identity, err := CaptureSession(group.ID())
	if err != nil {
		t.Fatalf("capture session: %v", err)
	}
	if identity.LeaderPID != group.ID() || identity.SessionID != group.ID() || identity.BirthToken == "" {
		t.Fatalf("session identity = %+v", identity)
	}
	quiescent, err := SessionQuiescent(identity)
	if err != nil || !quiescent {
		t.Fatalf("parked session quiescent=%v err=%v", quiescent, err)
	}
	empty, err := SessionEmpty(identity)
	if err != nil || empty {
		t.Fatalf("live parked session empty=%v err=%v", empty, err)
	}
	wrong := identity
	wrong.BirthToken += "-reused"
	if empty, err := SessionEmpty(wrong); err != nil || !empty {
		t.Fatalf("changed leader birth identity empty=%v err=%v, want original session gone", empty, err)
	}
}

func TestSessionEmptyTreatsReusedLeaderAsTheOriginalSessionGone(t *testing.T) {
	originalTable := sessionProcessTable
	originalIdentity := sessionIdentityLookup
	t.Cleanup(func() {
		sessionProcessTable = originalTable
		sessionIdentityLookup = originalIdentity
	})
	sessionProcessTable = func(context.Context, bool) ([]Info, error) {
		return []Info{{PID: 81, Group: 81, Session: 81, State: "R"}}, nil
	}
	sessionIdentityLookup = func(int) (int, string, error) {
		return 81, "new-birth", nil
	}

	empty, err := SessionEmpty(SessionIdentity{
		LeaderPID: 81, SessionID: 81, BirthToken: "original-birth",
	})
	if err != nil || !empty {
		t.Fatalf("reused session identity empty=%v err=%v, want original session gone", empty, err)
	}
}

func TestSessionEmptyRetainsAdmissionWhenReusedLeaderHasLiveSessionMembers(t *testing.T) {
	originalTable := sessionProcessTable
	originalIdentity := sessionIdentityLookup
	t.Cleanup(func() {
		sessionProcessTable = originalTable
		sessionIdentityLookup = originalIdentity
	})
	sessionProcessTable = func(context.Context, bool) ([]Info, error) {
		return []Info{
			{PID: 81, Group: 81, Session: 81, State: "R"},
			{PID: 93, Group: 93, Session: 81, State: "R"},
		}, nil
	}
	sessionIdentityLookup = func(int) (int, string, error) {
		return 81, "new-birth", nil
	}

	empty, err := SessionEmpty(SessionIdentity{
		LeaderPID: 81, SessionID: 81, BirthToken: "original-birth",
	})
	if err == nil && empty {
		t.Fatal("reused leader hid a live member of the guarded session")
	}
}

func TestTerminateSessionAllowsCooperativeCleanupBeforeEscalation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "cleanup-complete")
	ready := marker + ".ready"
	termSeen := marker + ".term"
	command := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	command.Env = append(os.Environ(), helperMode+"=session-cooperative", "SPARKWING_PROCGROUP_MARKER="+marker, procgroupReadyEnv+"="+ready, procgroupTermSeen+"="+termSeen)
	group, err := StartSession(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(t, group) })
	identity, err := CaptureSession(group.ID())
	if err != nil {
		t.Fatalf("capture cooperative session: %v", err)
	}
	waitForProcgroupReady(t, ready, time.Second)
	terminated := make(chan error, 1)
	terminateFinished := make(chan struct{})
	released := false
	go func() {
		err := TerminateSession(identity)
		close(terminateFinished)
		terminated <- err
	}()
	t.Cleanup(func() {
		if !released {
			select {
			case <-terminateFinished:
				released = true
			default:
				if err := syscall.Kill(group.ID(), syscall.SIGUSR1); err == nil || errors.Is(err, syscall.ESRCH) {
					released = true
				} else {
					t.Errorf("release cooperative cleanup: %v", err)
				}
			}
		}
		select {
		case <-terminateFinished:
		case <-time.After(2 * time.Second):
			t.Error("timed out joining cooperative termination")
		}
	})
	waitForProcgroupReady(t, termSeen, time.Second)
	observation := time.NewTimer(100 * time.Millisecond)
	defer observation.Stop()
	select {
	case err := <-terminated:
		released = true
		t.Fatalf("termination returned before cooperative cleanup was released: %v", err)
	case <-observation.C:
	}
	if err := syscall.Kill(group.ID(), syscall.SIGUSR1); err != nil {
		t.Fatalf("release cooperative cleanup: %v", err)
	}
	released = true
	select {
	case err := <-terminated:
		if err != nil {
			t.Fatalf("terminate cooperative session: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cooperative termination did not finish after cleanup release")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("cooperative cleanup did not finish before escalation: %v", err)
	}
}

func TestSessionTerminateKillsStubbornLeaderAndNestedGroup(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "descendant-ready")
	command := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	command.Env = append(os.Environ(), helperMode+"=session-stubborn", procgroupReadyEnv+"="+ready)
	group, err := StartSession(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(t, group) })
	waitForProcgroupReady(t, ready, 3*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := group.Terminate(ctx, 50*time.Millisecond); err != nil && (!group.Reaped() || !expectedHelperTermination(err)) {
		t.Fatalf("terminate stubborn session: %v", err)
	}
	if !group.Reaped() {
		t.Fatal("stubborn session leader was not reaped")
	}
}

func waitForProcgroupReady(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadlineAt := time.Now().Add(timeout)
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if !time.Now().Before(deadlineAt) {
			t.Fatalf("timed out waiting for process readiness at %s", path)
		}
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect process readiness at %s: %v", path, err)
		}
		poll.Reset(10 * time.Millisecond)
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for process readiness at %s", path)
		}
	}
}

func TestTerminateSessionReturnsOnlyAfterStubbornSessionIsEmpty(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	command.Env = append(os.Environ(), helperMode+"=session-stubborn")
	group, err := StartSession(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(t, group) })
	identity, err := CaptureSession(group.ID())
	if err != nil {
		t.Fatalf("capture stubborn session: %v", err)
	}
	if err := TerminateSession(identity); err != nil {
		t.Fatalf("terminate guarded session: %v", err)
	}
	empty, err := SessionEmpty(identity)
	if err != nil || !empty {
		t.Fatalf("terminated guarded session empty=%v err=%v", empty, err)
	}
}

func TestSessionCleanupIncludesNestedProcessGroups(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	command.Env = append(os.Environ(), helperMode+"=session-leader")
	group, err := StartSession(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(t, group) })
	select {
	case <-group.LeaderExited():
	case <-time.After(3 * time.Second):
		t.Fatal("session leader did not exit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := group.Finish(ctx, 50*time.Millisecond); err != nil {
		t.Fatalf("finish session: %v", err)
	}
	if !group.Reaped() {
		t.Fatal("session leader was not reaped")
	}
}

func TestGroupRetainsLeaderAnchorUntilDescendantsAreEmpty(t *testing.T) {
	group := startHelper(t, "leader")
	t.Cleanup(func() { terminateForTest(t, group) })
	select {
	case <-group.LeaderExited():
	case <-time.After(3 * time.Second):
		t.Fatal("leader did not exit")
	}
	if group.Reaped() {
		t.Fatal("leader was reaped before descendant cleanup")
	}
	if err := validateAnchor(group.ID(), true); err != nil {
		t.Fatalf("unreaped ownership anchor: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := group.Finish(ctx, 50*time.Millisecond); err != nil {
		t.Fatalf("finish group: %v", err)
	}
	if !group.Reaped() {
		t.Fatal("group leader was not reaped after descendants emptied")
	}
	if err := group.Kill(); err != nil {
		t.Fatalf("post-reap kill should be a no-op, got %v", err)
	}
}

func TestGroupCleanupFailureRetainsAnchorForRetry(t *testing.T) {
	group := startHelper(t, "short")
	t.Cleanup(func() { terminateForTest(t, group) })
	select {
	case <-group.LeaderExited():
	case <-time.After(3 * time.Second):
		t.Fatal("leader did not exit")
	}
	want := errors.New("injected membership failure")
	group.SetDescendantProbe(func(context.Context, int, bool, bool) (bool, error) { return false, want })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := group.Finish(ctx, 20*time.Millisecond)
	cancel()
	if !errors.Is(err, ErrCleanup) || !errors.Is(err, want) {
		t.Fatalf("finish error = %v, want retained cleanup failure", err)
	}
	if group.Reaped() {
		t.Fatal("failed cleanup reaped its ownership anchor")
	}
	if err := validateAnchor(group.ID(), true); err != nil {
		t.Fatalf("failed cleanup lost ownership anchor: %v", err)
	}
	group.SetDescendantProbe(nil)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	err = group.Finish(ctx, 20*time.Millisecond)
	cancel()
	if err != nil {
		t.Fatalf("retry finish: %v", err)
	}
	if !group.Reaped() {
		t.Fatal("successful retry did not reap the anchor")
	}
}

func TestGroupLifecycleStressLeavesEveryGroupReaped(t *testing.T) {
	const count = 50
	groups := make([]*Group, 0, count)
	for range count {
		group := startHelper(t, "leader")
		groups = append(groups, group)
		t.Cleanup(func() {
			if !group.Reaped() {
				terminateForTest(t, group)
			}
		})
	}
	type finishResult struct {
		id  int
		err error
	}
	results := make(chan finishResult, count)
	for _, group := range groups {
		go func(group *Group) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			results <- finishResult{id: group.ID(), err: group.Finish(ctx, 50*time.Millisecond)}
		}(group)
	}
	var firstFailure *finishResult
	for range count {
		result := <-results
		if result.err != nil && firstFailure == nil {
			firstFailure = &result
		}
	}
	if firstFailure != nil {
		t.Fatalf("finish group %d: %v", firstFailure.id, firstFailure.err)
	}
	for _, group := range groups {
		if !group.Reaped() {
			t.Fatalf("group %d was not reaped", group.ID())
		}
	}
}

func TestConcurrentFinishAndTerminateNeverLoseCompletedCleanup(t *testing.T) {
	const count = 50
	for iteration := range count {
		t.Run(fmt.Sprintf("iteration-%d", iteration), func(t *testing.T) {
			t.Parallel()
			group := startConcurrentCleanupHelper(t)
			results := make(chan error, 2)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				results <- group.Finish(ctx, 10*time.Millisecond)
			}()
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				results <- group.Terminate(ctx, 10*time.Millisecond)
			}()
			for range 2 {
				if err := <-results; err != nil && (!group.Reaped() || !expectedHelperTermination(err)) {
					t.Fatalf("completed concurrent cleanup reported failure: %v", err)
				}
			}
			if !group.Reaped() {
				t.Fatalf("group %d was not reaped", group.ID())
			}
		})
	}
}

func startHelper(t *testing.T, mode string) *Group {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	command.Env = append(os.Environ(), helperMode+"="+mode)
	group, err := Start(command)
	if err != nil {
		t.Fatal(err)
	}
	return group
}

func startConcurrentCleanupHelper(t *testing.T) *Group {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create helper readiness pipe: %v", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close readiness reader: %v", err)
		}
	}()
	command := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	command.Env = append(os.Environ(), helperMode+"=concurrent-cleanup", procgroupReadyFD+"=3")
	command.ExtraFiles = []*os.File{writer}
	group, err := Start(command)
	closeErr := writer.Close()
	if err != nil {
		t.Fatalf("start ready helper: %v", errors.Join(err, closeErr))
	}
	t.Cleanup(func() {
		if !group.Reaped() {
			terminateForTest(t, group)
		}
	})
	if err := errors.Join(closeErr, awaitProcgroupReadyByte(reader, 3*time.Second)); err != nil {
		terminateForTest(t, group)
		t.Fatalf("wait for helper readiness: %v", err)
	}
	return group
}

func terminateForTest(t *testing.T, group *Group) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := group.Terminate(ctx, 20*time.Millisecond); err != nil && (!group.Reaped() || !expectedHelperTermination(err)) {
		t.Errorf("terminate helper group: %v", err)
	}
}

func expectedHelperTermination(err error) bool {
	var exitErr *exec.ExitError
	if errors.Is(err, ErrCleanup) || !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && (status.Signal() == syscall.SIGTERM || status.Signal() == syscall.SIGKILL)
}

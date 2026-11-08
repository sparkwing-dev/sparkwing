//go:build !windows

package procgroup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const (
	helperMode       = "SPARKWING_PROCGROUP_HELPER"
	procgroupReadyFD = "SPARKWING_PROCGROUP_READY_FD"
)

func TestInfoTerminatedClassifiesKernelStates(t *testing.T) {
	for _, state := range []string{"Z", "X", "x"} {
		if !(Info{State: state}).Terminated() {
			t.Errorf("state %q is not classified as terminated", state)
		}
	}
	for _, state := range []string{"", "S", "R", "D"} {
		if (Info{State: state}).Terminated() {
			t.Errorf("state %q is classified as terminated", state)
		}
	}
}

func TestGroupHelperProcess(t *testing.T) {
	switch os.Getenv(helperMode) {
	case "descendant":
		IgnoreTermination()
		holdHelperProcess(os.Getenv(procgroupReadyFD))
	case "leader":
		if err := startReadyGroupDescendant(false); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		IgnoreTermination()
		os.Exit(0)
	case "short":
		os.Exit(0)
	case "concurrent-cleanup":
		IgnoreTermination()
		holdHelperProcess(os.Getenv(procgroupReadyFD))
	case "session-leader":
		if err := startReadyGroupDescendant(true); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	case "session-stubborn":
		IgnoreTermination()
		child := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
		child.Env = append(os.Environ(), helperMode+"=descendant")
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if os.Getenv(procgroupReadyFD) != "" {
			child.ExtraFiles = []*os.File{os.NewFile(3, "descendant-ready")}
		}
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		for _, file := range child.ExtraFiles {
			_ = file.Close()
		}
		holdHelperProcess("")
	case "session-parked":
		IgnoreTermination()
		holdHelperProcess("")
	case "session-cooperative":
		term := make(chan os.Signal, 1)
		release := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		signal.Notify(release, syscall.SIGUSR1)
		ready := os.NewFile(3, "cooperative-ready")
		if _, err := ready.Write([]byte{1}); err != nil {
			os.Exit(2)
		}
		<-term
		if _, err := ready.Write([]byte{1}); err != nil {
			os.Exit(2)
		}
		<-release
		if err := os.WriteFile(os.Getenv("SPARKWING_PROCGROUP_MARKER"), []byte("clean"), 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func holdHelperProcess(fdText string) {
	reader, writer, err := os.Pipe()
	if err != nil {
		os.Exit(2)
	}
	if fdText != "" {
		fd, err := strconv.Atoi(fdText)
		if err != nil {
			_ = reader.Close()
			os.Exit(2)
		}
		readyPipe := os.NewFile(uintptr(fd), "procgroup-helper-ready")
		if _, err := readyPipe.Write([]byte{1}); err != nil {
			_ = readyPipe.Close()
			_ = reader.Close()
			os.Exit(2)
		}
		_ = readyPipe.Close()
	}
	_, _ = reader.Read(make([]byte, 1))
	runtime.KeepAlive(writer)
	os.Exit(2)
}

func startReadyGroupDescendant(setpgid bool) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create descendant readiness pipe: %w", err)
	}
	defer reader.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	child.Env = append(os.Environ(), helperMode+"=descendant", procgroupReadyFD+"=3")
	child.ExtraFiles = []*os.File{writer}
	if setpgid {
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := child.Start(); err != nil {
		_ = writer.Close()
		return fmt.Errorf("start descendant: %w", err)
	}
	_ = writer.Close()
	readyErr := awaitProcgroupReadyByte(reader, 3*time.Second)
	if readyErr == nil {
		return nil
	}
	killErr := child.Process.Kill()
	waited := make(chan error, 1)
	go func() {
		waited <- child.Wait()
	}()
	join, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	select {
	case <-waited:
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return fmt.Errorf("%w; kill descendant: %v", readyErr, killErr)
		}
		return readyErr
	case <-join.Done():
		if killErr != nil {
			return fmt.Errorf("%w; kill descendant: %v; wait did not return", readyErr, killErr)
		}
		return fmt.Errorf("%w; descendant did not stop after kill", readyErr)
	}
}

func awaitProcgroupReadyByte(reader *os.File, timeout time.Duration) error {
	if err := reader.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return fmt.Errorf("read readiness signal: %w", err)
	}
	if buf[0] != 1 {
		return fmt.Errorf("invalid readiness signal: value=%d", buf[0])
	}
	return nil
}

func procgroupSignalPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	return reader, writer
}

func TestSessionIdentityBindsInspectionToLeaderBirth(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	cmd.Env = append(os.Environ(), helperMode+"=session-parked")
	group, err := StartSession(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(group) })
	identity, err := CaptureSession(group.ID())
	if err != nil {
		t.Fatalf("capture session: %v", err)
	}
	if identity.LeaderPID != group.ID() || identity.SessionID != group.ID() || identity.BirthToken == "" {
		t.Fatalf("session identity = %+v", identity)
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
	sessionProcessTable = func(bool) ([]Info, error) {
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
	sessionProcessTable = func(bool) ([]Info, error) {
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
	reader, writer := procgroupSignalPipe(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	cmd.Env = append(os.Environ(), helperMode+"=session-cooperative", "SPARKWING_PROCGROUP_MARKER="+marker, procgroupReadyFD+"=3")
	cmd.ExtraFiles = []*os.File{writer}
	group, err := StartSession(cmd)
	_ = writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(group) })
	identity, err := CaptureSession(group.ID())
	if err != nil {
		t.Fatalf("capture cooperative session: %v", err)
	}
	if err := awaitProcgroupReadyByte(reader, time.Second); err != nil {
		t.Fatalf("cooperative process readiness: %v", err)
	}
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
				if syscall.Kill(group.ID(), syscall.SIGUSR1) == nil {
					released = true
				}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		select {
		case <-terminateFinished:
		case <-ctx.Done():
			t.Error("timed out joining cooperative termination")
		}
	})
	if err := awaitProcgroupReadyByte(reader, time.Second); err != nil {
		t.Fatalf("cooperative process did not observe termination: %v", err)
	}
	select {
	case err := <-terminated:
		released = true
		t.Fatalf("termination returned before cooperative cleanup was released: %v", err)
	default:
	}
	if err := syscall.Kill(group.ID(), syscall.SIGUSR1); err != nil {
		t.Fatalf("release cooperative cleanup: %v", err)
	}
	released = true
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	select {
	case err := <-terminated:
		if err != nil {
			t.Fatalf("terminate cooperative session: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("cooperative termination did not finish after cleanup release")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("cooperative cleanup did not finish before escalation: %v", err)
	}
}

func TestSessionTerminateKillsStubbornLeaderAndNestedGroup(t *testing.T) {
	reader, writer := procgroupSignalPipe(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	cmd.Env = append(os.Environ(), helperMode+"=session-stubborn", procgroupReadyFD+"=3")
	cmd.ExtraFiles = []*os.File{writer}
	g, err := StartSession(cmd)
	_ = writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(g) })
	if err := awaitProcgroupReadyByte(reader, 3*time.Second); err != nil {
		t.Fatalf("descendant readiness: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := g.Terminate(ctx, 50*time.Millisecond); errors.Is(err, ErrCleanup) {
		t.Fatalf("terminate stubborn session: %v", err)
	}
	if !g.Reaped() {
		t.Fatal("stubborn session leader was not reaped")
	}
}

func TestTerminateSessionReturnsOnlyAfterStubbornSessionIsEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.0s of real work; the fast class runs under -short")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	cmd.Env = append(os.Environ(), helperMode+"=session-stubborn")
	group, err := StartSession(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(group) })
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
	ready, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	cmd.Env = append(os.Environ(), helperMode+"=session-leader")
	g, err := StartSession(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(g) })
	select {
	case <-g.LeaderExited():
	case <-ready.Done():
		t.Fatal("session leader did not exit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := g.Finish(ctx, 50*time.Millisecond); err != nil {
		t.Fatalf("finish session: %v", err)
	}
	if !g.Reaped() {
		t.Fatal("session leader was not reaped")
	}
}

func TestGroupRetainsLeaderAnchorUntilDescendantsAreEmpty(t *testing.T) {
	ready, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	g := startHelper(t, "leader")
	t.Cleanup(func() { terminateForTest(g) })
	select {
	case <-g.LeaderExited():
	case <-ready.Done():
		t.Fatal("leader did not exit")
	}
	if g.Reaped() {
		t.Fatal("leader was reaped before descendant cleanup")
	}
	if err := validateAnchor(g.ID(), true); err != nil {
		t.Fatalf("unreaped ownership anchor: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := g.Finish(ctx, 50*time.Millisecond); err != nil {
		t.Fatalf("finish group: %v", err)
	}
	if !g.Reaped() {
		t.Fatal("group leader was not reaped after descendants emptied")
	}
	if err := g.Kill(); err != nil {
		t.Fatalf("post-reap kill should be a no-op, got %v", err)
	}
}

func TestGroupCleanupFailureRetainsAnchorForRetry(t *testing.T) {
	ready, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	g := startHelper(t, "short")
	t.Cleanup(func() { terminateForTest(g) })
	select {
	case <-g.LeaderExited():
	case <-ready.Done():
		t.Fatal("leader did not exit")
	}
	want := errors.New("injected membership failure")
	g.SetDescendantProbe(func(int, bool, bool) (bool, error) { return false, want })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := g.Finish(ctx, 20*time.Millisecond)
	cancel()
	if !errors.Is(err, ErrCleanup) || !errors.Is(err, want) {
		t.Fatalf("finish error = %v, want retained cleanup failure", err)
	}
	if g.Reaped() {
		t.Fatal("failed cleanup reaped its ownership anchor")
	}
	if err := validateAnchor(g.ID(), true); err != nil {
		t.Fatalf("failed cleanup lost ownership anchor: %v", err)
	}
	g.SetDescendantProbe(nil)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	err = g.Finish(ctx, 20*time.Millisecond)
	cancel()
	if err != nil {
		t.Fatalf("retry finish: %v", err)
	}
	if !g.Reaped() {
		t.Fatal("successful retry did not reap the anchor")
	}
}

func TestGroupLifecycleStressLeavesEveryGroupReaped(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.7s of real work; the fast class runs under -short")
	}
	const count = 50
	groups := make([]*Group, 0, count)
	for range count {
		group := startHelper(t, "leader")
		groups = append(groups, group)
		t.Cleanup(func() {
			if !group.Reaped() {
				terminateForTest(group)
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
			results <- finishResult{id: group.ID(), err: group.Finish(t.Context(), 50*time.Millisecond)}
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
			g := startConcurrentCleanupHelper(t)
			results := make(chan error, 2)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				results <- g.Finish(ctx, 10*time.Millisecond)
			}()
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				results <- g.Terminate(ctx, 10*time.Millisecond)
			}()
			for range 2 {
				if err := <-results; errors.Is(err, ErrCleanup) {
					t.Fatalf("completed concurrent cleanup reported failure: %v", err)
				}
			}
			if !g.Reaped() {
				t.Fatalf("group %d was not reaped", g.ID())
			}
		})
	}
}

func startHelper(t *testing.T, mode string) *Group {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	cmd.Env = append(os.Environ(), helperMode+"="+mode)
	g, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func startConcurrentCleanupHelper(t *testing.T) *Group {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create helper readiness pipe: %v", err)
	}
	defer reader.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	cmd.Env = append(os.Environ(), helperMode+"=concurrent-cleanup", procgroupReadyFD+"=3")
	cmd.ExtraFiles = []*os.File{writer}
	g, err := Start(cmd)
	_ = writer.Close()
	if err != nil {
		t.Fatalf("start ready helper: %v", err)
	}
	t.Cleanup(func() {
		if !g.Reaped() {
			terminateForTest(g)
		}
	})
	if err := awaitProcgroupReadyByte(reader, 3*time.Second); err != nil {
		terminateForTest(g)
		t.Fatalf("wait for helper readiness: %v", err)
	}
	return g
}

func terminateForTest(g *Group) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = g.Terminate(ctx, 20*time.Millisecond)
}

//go:build !windows

package procgroup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

const helperMode = "SPARKWING_PROCGROUP_HELPER"

func TestGroupHelperProcess(t *testing.T) {
	switch os.Getenv(helperMode) {
	case "descendant":
		IgnoreTermination()
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "leader":
		child := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
		child.Env = append(os.Environ(), helperMode+"=descendant")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		IgnoreTermination()
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	case "short":
		os.Exit(0)
	case "ignore-short":
		IgnoreTermination()
		time.Sleep(50 * time.Millisecond)
		os.Exit(0)
	case "session-leader":
		child := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
		child.Env = append(os.Environ(), helperMode+"=descendant")
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	case "session-stubborn":
		child := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
		child.Env = append(os.Environ(), helperMode+"=descendant")
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		IgnoreTermination()
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}

func TestSessionTerminateKillsStubbornLeaderAndNestedGroup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	cmd.Env = append(os.Environ(), helperMode+"=session-stubborn")
	g, err := StartSession(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(g) })
	time.Sleep(150 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := g.Terminate(ctx, 50*time.Millisecond); errors.Is(err, ErrCleanup) {
		t.Fatalf("terminate stubborn session: %v", err)
	}
	if !g.Reaped() {
		t.Fatal("stubborn session leader was not reaped")
	}
}

func TestSessionCleanupIncludesNestedProcessGroups(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestGroupHelperProcess$")
	cmd.Env = append(os.Environ(), helperMode+"=session-leader")
	g, err := StartSession(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateForTest(g) })
	select {
	case <-g.LeaderExited():
	case <-time.After(3 * time.Second):
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
	g := startHelper(t, "leader")
	t.Cleanup(func() { terminateForTest(g) })
	select {
	case <-g.LeaderExited():
	case <-time.After(3 * time.Second):
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
	g := startHelper(t, "short")
	t.Cleanup(func() { terminateForTest(g) })
	select {
	case <-g.LeaderExited():
	case <-time.After(3 * time.Second):
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
	const count = 50
	groups := make([]*Group, 0, count)
	for range count {
		groups = append(groups, startHelper(t, "leader"))
	}
	for _, group := range groups {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := group.Finish(ctx, 50*time.Millisecond)
		cancel()
		if err != nil {
			t.Fatalf("finish group %d: %v", group.ID(), err)
		}
	}
	for _, group := range groups {
		if !group.Reaped() {
			t.Fatalf("group %d was not reaped", group.ID())
		}
	}
}

func TestConcurrentFinishAndTerminateNeverLoseCompletedCleanup(t *testing.T) {
	const count = 50
	for range count {
		g := startHelper(t, "ignore-short")
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

func terminateForTest(g *Group) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = g.Terminate(ctx, 20*time.Millisecond)
}

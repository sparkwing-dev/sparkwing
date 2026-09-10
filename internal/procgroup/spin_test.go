//go:build !windows

package procgroup

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackoffPollDoublesUpToItsCap(t *testing.T) {
	poll := newBackoffPoll(10*time.Millisecond, 80*time.Millisecond)
	want := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
		80 * time.Millisecond,
		80 * time.Millisecond,
	}
	for i, expect := range want {
		if got := poll.next(); got != expect {
			t.Fatalf("poll %d = %s, want %s", i, got, expect)
		}
	}
}

func TestWaitDescendantsEmptyBacksOffWhileTheTreeRefusesToDie(t *testing.T) {
	const window = 500 * time.Millisecond
	probes := &atomic.Int64{}
	g := &Group{id: 4242}
	g.SetDescendantProbe(func(int, bool, bool) (bool, error) {
		probes.Add(1)
		return false, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	if err := g.waitDescendantsEmpty(ctx); err == nil {
		t.Fatal("wait returned success while descendants remained")
	}

	unpaced := int64(window / guardedSessionPollInterval)
	if got := probes.Load(); got >= unpaced/2 {
		t.Fatalf("process-table probes = %d in %s; an unpaced wait would be about %d", got, window, unpaced)
	}
	if got := probes.Load(); got < 2 {
		t.Fatalf("process-table probes = %d; the wait stopped watching", got)
	}
}

func TestWaitDescendantsEmptyStillAnswersQuickly(t *testing.T) {
	probes := &atomic.Int64{}
	g := &Group{id: 4243}
	g.SetDescendantProbe(func(int, bool, bool) (bool, error) {
		return probes.Add(1) > 1, nil
	})

	start := time.Now()
	if err := g.waitDescendantsEmpty(context.Background()); err != nil {
		t.Fatalf("wait for an emptied group: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("observing an emptied group took %s, want about the base poll interval", elapsed)
	}
}

func TestSessionEmptyAnswersLeaderIdentityFromItsOwnSnapshot(t *testing.T) {
	originalTable := sessionProcessTable
	originalIdentity := sessionIdentityLookup
	t.Cleanup(func() {
		sessionProcessTable = originalTable
		sessionIdentityLookup = originalIdentity
	})
	sessionProcessTable = func(bool) ([]Info, error) {
		return []Info{{PID: 81, Group: 81, Session: 81, State: "R", Birth: "birth-81"}}, nil
	}
	sessionIdentityLookup = func(int) (int, string, error) {
		t.Fatal("a snapshot carrying birth tokens asked the kernel again")
		return 0, "", nil
	}

	empty, err := SessionEmpty(SessionIdentity{LeaderPID: 81, SessionID: 81, BirthToken: "birth-81"})
	if err != nil || empty {
		t.Fatalf("live session empty=%v err=%v, want it held", empty, err)
	}
	reused, err := SessionEmpty(SessionIdentity{LeaderPID: 81, SessionID: 81, BirthToken: "older-birth"})
	if err != nil || !reused {
		t.Fatalf("reused leader empty=%v err=%v, want the original session gone", reused, err)
	}
}

func TestLeaderExitDuringInspectionIsAnAnswerNotAFailure(t *testing.T) {
	originalTable := sessionProcessTable
	originalIdentity := sessionIdentityLookup
	t.Cleanup(func() {
		sessionProcessTable = originalTable
		sessionIdentityLookup = originalIdentity
	})
	sessionProcessTable = func(bool) ([]Info, error) {
		return []Info{{PID: 81, Group: 81, Session: 81, State: "R"}}, nil
	}
	sessionIdentityLookup = func(pid int) (int, string, error) {
		return 0, "", fmt.Errorf("%w: process %d", ErrProcessAbsent, pid)
	}

	empty, err := SessionEmpty(SessionIdentity{LeaderPID: 81, SessionID: 81, BirthToken: "birth-81"})
	if err != nil {
		t.Fatalf("a leader that exited mid-inspection reported an error: %v", err)
	}
	if empty {
		t.Fatal("session reported empty while the snapshot still showed a live member")
	}

	sessionProcessTable = func(bool) ([]Info, error) { return nil, nil }
	empty, err = SessionEmpty(SessionIdentity{LeaderPID: 81, SessionID: 81, BirthToken: "birth-81"})
	if err != nil || !empty {
		t.Fatalf("departed session empty=%v err=%v, want it empty", empty, err)
	}
}

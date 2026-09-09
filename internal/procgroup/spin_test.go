//go:build !windows

package procgroup

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

func TestCaptureSessionTableCostsOneListingForManySessions(t *testing.T) {
	originalTable := sessionProcessTable
	originalIdentity := sessionIdentityLookup
	t.Cleanup(func() {
		sessionProcessTable = originalTable
		sessionIdentityLookup = originalIdentity
	})
	listings := 0
	sessionProcessTable = func(context.Context, bool) ([]Info, error) {
		listings++
		return []Info{{PID: 81, Group: 81, Session: 81, State: "R"}}, nil
	}
	sessionIdentityLookup = func(int) (int, string, error) { return 81, "birth-81", nil }

	table, err := CaptureSessionTable()
	if err != nil {
		t.Fatalf("capture session table: %v", err)
	}
	liveSessionEmpty, err := table.SessionEmpty(SessionIdentity{LeaderPID: 81, SessionID: 81, BirthToken: "birth-81"})
	if err != nil || liveSessionEmpty {
		t.Fatalf("live session empty=%v err=%v, want it held", liveSessionEmpty, err)
	}
	gone, err := table.SessionEmpty(SessionIdentity{LeaderPID: 90, SessionID: 90, BirthToken: "birth-90"})
	if err != nil || !gone {
		t.Fatalf("absent session empty=%v err=%v, want it empty", gone, err)
	}
	if listings != 1 {
		t.Fatalf("process-table listings = %d, want 1 for the whole snapshot", listings)
	}
}

func TestWaitDescendantsEmptyBacksOffUntilDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const window = 500 * time.Millisecond
		probes := &atomic.Int64{}
		group := &Group{id: 4242}
		group.SetDescendantProbe(func(context.Context, int, bool, bool) (bool, error) {
			probes.Add(1)
			return false, nil
		})

		ctx, cancel := context.WithTimeout(context.Background(), window)
		defer cancel()
		if err := group.waitDescendantsEmpty(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait error = %v, want deadline exceeded", err)
		}

		unpaced := int64(window / guardedSessionPollInterval)
		if got := probes.Load(); got >= unpaced/2 {
			t.Fatalf("process-table probes = %d in %s; an unpaced wait would be about %d", got, window, unpaced)
		}
		if got := probes.Load(); got < 2 {
			t.Fatalf("process-table probes = %d; want repeated inspection", got)
		}
	})
}

func TestWaitDescendantsEmptyUsesInitialPollInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		probes := &atomic.Int64{}
		group := &Group{id: 4243}
		group.SetDescendantProbe(func(context.Context, int, bool, bool) (bool, error) {
			return probes.Add(1) > 1, nil
		})

		start := time.Now()
		if err := group.waitDescendantsEmpty(context.Background()); err != nil {
			t.Fatalf("wait for an emptied group: %v", err)
		}
		if elapsed := time.Since(start); elapsed != guardedSessionPollInterval {
			t.Fatalf("observing an emptied group took %s, want the initial poll interval", elapsed)
		}
	})
}

func TestSessionTableAnswersLeaderIdentityFromItsOwnSnapshot(t *testing.T) {
	originalTable := sessionProcessTable
	originalIdentity := sessionIdentityLookup
	t.Cleanup(func() {
		sessionProcessTable = originalTable
		sessionIdentityLookup = originalIdentity
	})
	sessionProcessTable = func(context.Context, bool) ([]Info, error) {
		return []Info{{PID: 81, Group: 81, Session: 81, State: "R", Birth: "birth-81"}}, nil
	}
	sessionIdentityLookup = func(int) (int, string, error) {
		t.Fatal("a snapshot carrying birth tokens asked the kernel again")
		return 0, "", nil
	}

	table, err := CaptureSessionTable()
	if err != nil {
		t.Fatalf("capture session table: %v", err)
	}
	empty, err := table.SessionEmpty(SessionIdentity{LeaderPID: 81, SessionID: 81, BirthToken: "birth-81"})
	if err != nil || empty {
		t.Fatalf("live guarded session empty=%v err=%v, want it held", empty, err)
	}
	reusedSessionEmpty, err := table.SessionEmpty(SessionIdentity{LeaderPID: 81, SessionID: 81, BirthToken: "older-birth"})
	if err != nil || !reusedSessionEmpty {
		t.Fatalf("reused leader empty=%v err=%v, want the original session gone", reusedSessionEmpty, err)
	}
}

func TestLeaderExitDuringInspectionIsAnAnswerNotAFailure(t *testing.T) {
	originalTable := sessionProcessTable
	originalIdentity := sessionIdentityLookup
	t.Cleanup(func() {
		sessionProcessTable = originalTable
		sessionIdentityLookup = originalIdentity
	})
	sessionProcessTable = func(context.Context, bool) ([]Info, error) {
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

	sessionProcessTable = func(context.Context, bool) ([]Info, error) { return nil, nil }
	empty, err = SessionEmpty(SessionIdentity{LeaderPID: 81, SessionID: 81, BirthToken: "birth-81"})
	if err != nil || !empty {
		t.Fatalf("departed session empty=%v err=%v, want it empty", empty, err)
	}
}

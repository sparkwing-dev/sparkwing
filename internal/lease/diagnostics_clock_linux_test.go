package lease

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func clockMonotonic(t *testing.T) int64 {
	t.Helper()
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &now); err != nil {
		t.Fatal(err)
	}
	return now.Nano()
}

func TestKeeperDiagnosticsBracketTheOriginOnClockMonotonic(t *testing.T) {
	diagnostics, recorded := collectDiagnostics(t)
	trace := diagnostics.start(t.Context(), Identity{Role: "OWNER", Run: "r"})
	before := clockMonotonic(t)
	trace.emit("watch_arm", time.Time{}, time.Now().Add(time.Minute), "", 0)
	after := clockMonotonic(t)
	all, _ := sealed(t, diagnostics, recorded)
	arms := ofKind(all, "watch_arm")
	if len(arms) != 1 {
		t.Fatalf("records=%+v", all)
	}
	event := arms[0]
	if event.OriginMonoBefore <= 0 || event.OriginMonoBefore > event.OriginMonoAfter {
		t.Fatalf("origin bracket [%d, %d]", event.OriginMonoBefore, event.OriginMonoAfter)
	}
	// safety: the sample lies in [before, after], and the origin's CLOCK_MONOTONIC lies in its bracket.
	if event.OriginMonoBefore+event.MonoBefore > after || event.OriginMonoAfter+event.MonoAfter < before {
		t.Fatalf("event [%d, %d] + origin [%d, %d] misses CLOCK_MONOTONIC [%d, %d]",
			event.MonoBefore, event.MonoAfter, event.OriginMonoBefore, event.OriginMonoAfter, before, after)
	}
}

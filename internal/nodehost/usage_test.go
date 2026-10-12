package nodehost

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
)

type proc = nodemetrics.SessionProcess

func scripted(t *testing.T, snapshots ...map[int]proc) *UsageSampler {
	t.Helper()
	next := 0
	return &UsageSampler{sid: 10, snapshot: func(sid int) (map[int]proc, bool) {
		if sid != 10 {
			t.Fatalf("snapshot of session %d, want 10", sid)
		}
		snap := snapshots[next]
		next++
		return snap, snap != nil
	}}
}

func sampleAll(s *UsageSampler, n int) Usage {
	for range n {
		s.sample()
	}
	return s.finish(nil)
}

func TestUsageCountsReapedChildOnce(t *testing.T) {
	s := scripted(t,
		map[int]proc{10: {Parent: 1, Birth: 1, CPU: time.Second}, 11: {Parent: 10, Birth: 2, CPU: 2 * time.Second}},
		map[int]proc{10: {Parent: 1, Birth: 1, CPU: time.Second, Reaped: 2 * time.Second}},
	)
	if got := sampleAll(s, 2).CPUTime; got != 3*time.Second {
		t.Fatalf("CPU = %v, want 3s", got)
	}
}

func TestUsageCountsOrphanReapedOutsideSession(t *testing.T) {
	s := scripted(t,
		map[int]proc{
			10: {Parent: 1, Birth: 1, CPU: time.Second},
			11: {Parent: 10, Birth: 2, CPU: time.Second},
			12: {Parent: 11, Birth: 3, CPU: time.Second},
		},
		map[int]proc{
			10: {Parent: 1, Birth: 1, CPU: time.Second, Reaped: time.Second},
			12: {Parent: 1, Birth: 3, CPU: 3 * time.Second},
		},
		map[int]proc{10: {Parent: 1, Birth: 1, CPU: time.Second, Reaped: time.Second}},
	)
	if got := sampleAll(s, 3).CPUTime; got != 5*time.Second {
		t.Fatalf("CPU = %v, want 5s", got)
	}
}

func TestUsageTreatsReusedPIDAsNewMember(t *testing.T) {
	s := scripted(t,
		map[int]proc{10: {Parent: 1, Birth: 1}, 11: {Parent: 1, Birth: 2, CPU: time.Second}},
		map[int]proc{10: {Parent: 1, Birth: 1}, 11: {Parent: 1, Birth: 9, CPU: time.Second}},
	)
	if got := sampleAll(s, 2).CPUTime; got != 2*time.Second {
		t.Fatalf("CPU = %v, want 2s", got)
	}
}

func TestUsagePeakRSSSumsLiveMembersAndSkipsFailedSnapshots(t *testing.T) {
	s := scripted(t,
		map[int]proc{10: {Parent: 1, Birth: 1, RSS: 10}, 11: {Parent: 10, Birth: 2, RSS: 30}},
		nil,
		map[int]proc{10: {Parent: 1, Birth: 1, RSS: 20}},
	)
	u := sampleAll(s, 3)
	if u.MaxRSSBytes != 40 || !u.SessionWide {
		t.Fatalf("usage = %+v, want peak 40 session-wide", u)
	}
}

func TestUsageWithoutSessionSnapshotsFallsBackToLeader(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestUsageHelperProcess$")
	cmd.Env = append(os.Environ(), "SW_USAGE_HELPER=exit")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	s := startUsage(10, func(int) (map[int]proc, bool) { return nil, false })
	u := s.Stop(cmd.ProcessState)
	if u.SessionWide {
		t.Fatal("SessionWide with no successful snapshot")
	}
	if want := cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime(); u.CPUTime != want {
		t.Fatalf("CPU = %v, want leader's %v", u.CPUTime, want)
	}
	if again := s.Stop(nil); again != u {
		t.Fatalf("second Stop = %+v, want %+v", again, u)
	}
}

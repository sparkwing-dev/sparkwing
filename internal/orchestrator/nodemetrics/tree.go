package nodemetrics

import (
	"math"
	"math/bits"
	"time"
)

type processSample struct {
	parent int
	birth  uint64
	cpu    time.Duration
	reaped time.Duration
	rss    int64
	valid  bool
}

// SessionProcess is one member of a process session at a snapshot. CPU is the
// member's own user and system time since birth; Reaped is the time of the
// children it has waited for.
type SessionProcess struct {
	Parent int
	Birth  uint64
	CPU    time.Duration
	Reaped time.Duration
	RSS    int64
}

// SessionProcesses reads surviving session members, returning false on unreadable members.
func SessionProcesses(sid int) (map[int]SessionProcess, bool) {
	if sid <= 0 {
		return nil, false
	}
	processes, ok := snapshotProcesses(func(processes map[int]processSample) map[int]processSample {
		return sessionMembers(sid, processes)
	})
	if !ok {
		return nil, false
	}
	members := make(map[int]SessionProcess, len(processes))
	for pid, p := range processes {
		if !p.valid {
			return nil, false
		}
		members[pid] = SessionProcess{Parent: p.parent, Birth: p.birth, CPU: p.cpu, Reaped: p.reaped, RSS: p.rss}
	}
	return members, true
}

func cpuTicks(ticks, frequency uint64) (time.Duration, bool) {
	if frequency == 0 {
		return 0, false
	}
	hi, lo := bits.Mul64(ticks, uint64(time.Second))
	if hi >= frequency {
		return 0, false
	}
	ns, _ := bits.Div64(hi, lo, frequency)
	if ns > math.MaxInt64 {
		return 0, false
	}
	return time.Duration(ns), true
}

package procusage

import (
	"math"
	"testing"
	"time"
)

func fixtureProcess(pid, parent int, birth uint64, cpu, reaped time.Duration, rss int64, at time.Time) Process {
	return Process{Identity: Identity{PID: pid, Birth: birth}, Parent: parent, CPU: cpu, ReapedCPU: reaped, RSS: rss, ReadAt: at, CPUAvailable: true, ReapedAvailable: true, RSSAvailable: true, AncestryAvailable: true, IdentityAvailable: true}
}

func fixtureSnapshot(at time.Time, processes ...Process) Snapshot {
	s := Snapshot{Start: at, End: at, Available: true, Processes: make(map[int]Process)}
	for _, p := range processes {
		s.Processes[p.PID] = p
	}
	return s
}

func TestTreeReapingDoesNotRepeatCPU(t *testing.T) {
	start := time.Now()
	tree := Tree{PID: 10}
	tree.Observe(fixtureSnapshot(start, fixtureProcess(10, 1, 10, time.Second, 0, 100, start), fixtureProcess(20, 10, 20, 8*time.Second, 0, 200, start)))
	now := start.Add(time.Second)
	r := tree.Observe(fixtureSnapshot(now, fixtureProcess(10, 1, 10, 2*time.Second, 10*time.Second, 100, now)))
	if r.CPUMillicores != 1000 || r.CPUQuality != "partial" || r.RSSBytes != 100 {
		t.Fatalf("reaped child must not enter live CPU intervals: %+v", r)
	}
}

func TestTreeNonpositiveIntervalsDoNotBecomeMeasuredZero(t *testing.T) {
	for _, elapsed := range []time.Duration{0, -time.Second} {
		t.Run(elapsed.String(), func(t *testing.T) {
			start := time.Now()
			tree := Tree{PID: 10}
			tree.Observe(fixtureSnapshot(start, fixtureProcess(10, 1, 10, 0, 0, 100, start)))
			now := start.Add(elapsed)
			reading := tree.Observe(fixtureSnapshot(now, fixtureProcess(10, 1, 10, time.Second, 0, 100, now)))
			if reading.CPUMillicores != 0 || reading.CPUQuality != "unavailable" || reading.MemoryQuality != "sampled" || reading.RSSBytes != 100 {
				t.Fatalf("invalid CPU interval must retain memory and unavailable CPU: %+v", reading)
			}
		})
	}
}

func TestTreeKeepsKnownReparentedDescendants(t *testing.T) {
	start := time.Now()
	tree := Tree{PID: 10}
	tree.Observe(fixtureSnapshot(start, fixtureProcess(10, 1, 10, 0, 0, 100, start), fixtureProcess(20, 10, 20, 0, 0, 200, start), fixtureProcess(30, 20, 30, 0, 0, 300, start)))
	now := start.Add(time.Second)
	r := tree.Observe(fixtureSnapshot(now, fixtureProcess(10, 1, 10, 0, 0, 100, now), fixtureProcess(30, 1, 30, time.Second, 0, 300, now)))
	if r.CPUMillicores != 1000 || r.RSSBytes != 400 || r.Processes != 2 || r.CPUQuality != "partial" {
		t.Fatalf("known orphan was lost: %+v", r)
	}
}

func TestTreePIDReuseDoesNotCarryBaselineOrOwnership(t *testing.T) {
	start := time.Now()
	tree := Tree{PID: 10}
	tree.Observe(fixtureSnapshot(start, fixtureProcess(10, 1, 10, 0, 0, 100, start), fixtureProcess(20, 10, 20, time.Second, 0, 200, start)))
	now := start.Add(time.Second)
	r := tree.Observe(fixtureSnapshot(now, fixtureProcess(10, 1, 10, 0, 0, 100, now), fixtureProcess(20, 1, 21, 10*time.Second, 0, 500, now)))
	if r.CPUMillicores != 0 || r.RSSBytes != 100 || r.CPUQuality != "partial" {
		t.Fatalf("replacement inherited old ownership: %+v", r)
	}
	r = tree.Observe(fixtureSnapshot(now.Add(time.Second), fixtureProcess(10, 1, 11, 20*time.Second, 0, 100, now)))
	if r.CPUQuality != "unavailable" || r.MemoryQuality != "unavailable" {
		t.Fatalf("replaced root was accepted: %+v", r)
	}
}

func TestTreeMeasurementFailuresRemainPartial(t *testing.T) {
	for _, failure := range []string{"cpu", "rss", "regression", "ancestry"} {
		t.Run(failure, func(t *testing.T) {
			start := time.Now()
			tree := Tree{PID: 10}
			tree.Observe(fixtureSnapshot(start, fixtureProcess(10, 1, 10, 0, 0, 100, start), fixtureProcess(20, 10, 20, time.Second, 0, 200, start)))
			now := start.Add(time.Second)
			child := fixtureProcess(20, 10, 20, time.Second, 0, 200, now)
			switch failure {
			case "cpu":
				child.CPUAvailable = false
			case "rss":
				child.RSSAvailable = false
			case "regression":
				child.CPU = 0
			case "ancestry":
				child.AncestryAvailable = false
			}
			r := tree.Observe(fixtureSnapshot(now, fixtureProcess(10, 1, 10, 0, 0, 100, now), child))
			if failure == "rss" {
				if r.MemoryQuality != "partial" || r.CPUQuality != "sampled" {
					t.Fatalf("wrong independent quality: %+v", r)
				}
			} else if r.CPUQuality != "partial" {
				t.Fatalf("missing CPU became measured zero: %+v", r)
			}
		})
	}
}

func TestTicksDurationUsesReportedFrequencyAndRejectsOverflow(t *testing.T) {
	for _, test := range []struct {
		ticks, frequency uint64
		want             time.Duration
		ok               bool
	}{
		{24_000_000, 24_000_000, time.Second, true},
		{150, 100, 1500 * time.Millisecond, true},
		{0, 100, 0, true},
		{1, 0, 0, false},
		{math.MaxUint64, 1, 0, false},
		{math.MaxInt64, 1_000_000_000, math.MaxInt64, true},
	} {
		got, ok := ticksDuration(test.ticks, test.frequency)
		if got != test.want || ok != test.ok {
			t.Fatalf("ticksDuration(%d, %d) = %d, %t; want %d, %t", test.ticks, test.frequency, got, ok, test.want, test.ok)
		}
	}
}

func TestTreeUnreadableKnownOrphanMakesMemoryPartial(t *testing.T) {
	start := time.Now()
	tree := Tree{PID: 10}
	tree.Observe(fixtureSnapshot(start, fixtureProcess(10, 1, 10, 0, 0, 100, start), fixtureProcess(20, 10, 20, 0, 0, 200, start)))
	now := start.Add(time.Second)
	r := tree.Observe(fixtureSnapshot(now, fixtureProcess(10, 1, 10, 0, 0, 100, now), Process{Identity: Identity{PID: 20}, ReadAt: now}))
	if r.MemoryQuality != "partial" || r.CPUQuality != "partial" || r.RSSBytes != 100 {
		t.Fatalf("unreadable owned process became measured absence: %+v", r)
	}
}

func TestTreeRejectsChildOfReusedParent(t *testing.T) {
	now := time.Now()
	tree := Tree{PID: 10}
	r := tree.Observe(fixtureSnapshot(now, fixtureProcess(10, 1, 10, 0, 0, 100, now), fixtureProcess(20, 10, 40, 0, 0, 200, now), fixtureProcess(30, 20, 30, 0, 0, 300, now)))
	if r.RSSBytes != 300 || r.Processes != 2 {
		t.Fatalf("stale parent PID attributed unrelated child: %+v", r)
	}
}

func TestTreeUnseenReapedChildMakesCPUIntervalPartial(t *testing.T) {
	start := time.Now()
	tree := Tree{PID: 10}
	tree.Observe(fixtureSnapshot(start, fixtureProcess(10, 1, 10, 0, 0, 100, start)))
	now := start.Add(time.Second)
	r := tree.Observe(fixtureSnapshot(now, fixtureProcess(10, 1, 10, time.Millisecond, 100*time.Millisecond, 100, now)))
	if r.CPUMillicores != 1 || r.CPUQuality != "partial" {
		t.Fatalf("known missing child became a sampled zero or a completion spike: %+v", r)
	}
}

func TestTreeAddsIndependentProcessRates(t *testing.T) {
	start := time.Now()
	tree := Tree{PID: 10}
	first := fixtureSnapshot(start,
		fixtureProcess(10, 1, 10, 7*time.Second, 0, 100, start),
		fixtureProcess(20, 10, 20, 11*time.Second, 0, 200, start.Add(250*time.Millisecond)),
		fixtureProcess(30, 20, 30, 13*time.Second, 0, 300, start.Add(500*time.Millisecond)))
	first.End = start.Add(500 * time.Millisecond)
	tree.Observe(first)
	second := fixtureSnapshot(start.Add(1250*time.Millisecond),
		fixtureProcess(10, 1, 10, 8*time.Second, 0, 150, start.Add(2*time.Second)),
		fixtureProcess(20, 10, 20, 12*time.Second, 0, 250, start.Add(1250*time.Millisecond)),
		fixtureProcess(30, 20, 30, 15*time.Second, 0, 350, start.Add(1500*time.Millisecond)))
	second.End = start.Add(2 * time.Second)
	r := tree.Observe(second)
	if r.CPUMillicores != 3500 || r.RSSBytes != 750 || r.Processes != 3 || r.CPUQuality != "sampled" || r.MemoryQuality != "sampled" {
		t.Fatalf("expected 500 + 1000 + 2000 millicores and 150 + 250 + 350 bytes: %+v", r)
	}
	if !r.Start.Equal(first.Start) || !r.End.Equal(second.End) {
		t.Fatalf("observation window [%s, %s] does not cover both scans [%s, %s]", r.Start, r.End, first.Start, second.End)
	}
}

func TestTreeNewChildDoesNotChargeItsPriorCPU(t *testing.T) {
	start := time.Now()
	tree := Tree{PID: 10}
	tree.Observe(fixtureSnapshot(start, fixtureProcess(10, 1, 10, 0, 0, 100, start)))
	now := start.Add(time.Second)
	r := tree.Observe(fixtureSnapshot(now, fixtureProcess(10, 1, 10, time.Second, 0, 100, now), fixtureProcess(20, 10, 20, 100*time.Second, 0, 200, now)))
	if r.CPUMillicores != 1000 || r.CPUQuality != "partial" || r.RSSBytes != 300 {
		t.Fatalf("new child's lifetime CPU entered the interval: %+v", r)
	}
}

func TestTreeRecoveryRequiresFreshBaselines(t *testing.T) {
	for _, failure := range []string{"snapshot", "member"} {
		t.Run(failure, func(t *testing.T) {
			start := time.Now()
			tree := Tree{PID: 10}
			for tick := 0; tick < 5; tick++ {
				now := start.Add(time.Duration(tick) * time.Second)
				p := fixtureProcess(10, 1, 10, time.Duration(tick)*time.Second, 0, 100, now)
				s := fixtureSnapshot(now, p)
				if tick == 2 {
					if failure == "snapshot" {
						s.Available = false
					} else {
						p.CPUAvailable = false
						s.Processes[10] = p
					}
				}
				r := tree.Observe(s)
				if tick == 1 || tick == 4 {
					if r.CPUMillicores != 1000 || r.CPUQuality != "sampled" {
						t.Fatalf("tick %d: %+v", tick, r)
					}
				} else if r.CPUMillicores != 0 || r.CPUQuality != "unavailable" {
					t.Fatalf("tick %d bridged missing CPU: %+v", tick, r)
				}
			}
		})
	}
}

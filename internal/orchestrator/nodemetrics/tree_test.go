package nodemetrics

import (
	"maps"
	"math"
	"testing"
	"time"
)

func TestTreeUsageCountsLiveAndReapedCPUOnce(t *testing.T) {
	processes := map[int]processSample{
		10: {parent: 1, birth: 100, cpu: time.Second, reaped: 2 * time.Second, rss: 40, valid: true},
		11: {parent: 10, birth: 101, cpu: 3 * time.Second, reaped: 4 * time.Second, rss: 60, valid: true},
		12: {parent: 11, birth: 102, cpu: 5 * time.Second, rss: 80, valid: true},
		20: {parent: 1, birth: 200, cpu: 99 * time.Second, rss: 999, valid: true},
	}
	cpu, memory, ok := treeUsage(10, processes, processes)
	if !ok || cpu != 15*time.Second || memory != 180 {
		t.Fatalf("live tree = %v, %d, %v; want 15s, 180, true", cpu, memory, ok)
	}
	delete(processes, 12)
	p := processes[11]
	p.reaped = 9 * time.Second
	processes[11] = p
	cpu, memory, ok = treeUsage(10, processes, processes)
	if !ok || cpu != 15*time.Second || memory != 100 {
		t.Fatalf("reaped grandchild = %v, %d, %v; want 15s, 100, true", cpu, memory, ok)
	}
	delete(processes, 11)
	p = processes[10]
	p.reaped = 14 * time.Second
	processes[10] = p
	cpu, memory, ok = treeUsage(10, processes, processes)
	if !ok || cpu != 15*time.Second || memory != 40 {
		t.Fatalf("reaped child = %v, %d, %v; want 15s, 40, true", cpu, memory, ok)
	}
}

func TestTreeUsageRejectsChangingOrInvalidScans(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[int]processSample)
	}{
		{"missing root", func(p map[int]processSample) { delete(p, 10) }},
		{"child exited", func(p map[int]processSample) { delete(p, 11) }},
		{"child started", func(p map[int]processSample) { p[12] = processSample{parent: 10, birth: 3, valid: true} }},
		{"pid reused", func(p map[int]processSample) { q := p[11]; q.birth++; p[11] = q }},
		{"child reparented", func(p map[int]processSample) { q := p[11]; q.parent = 1; p[11] = q }},
		{"cpu reset", func(p map[int]processSample) { q := p[11]; q.cpu = 0; p[11] = q }},
		{"reaped during scan", func(p map[int]processSample) { q := p[10]; q.reaped++; p[10] = q }},
		{"reader failed", func(p map[int]processSample) { q := p[11]; q.valid = false; p[11] = q }},
		{"negative memory", func(p map[int]processSample) { q := p[11]; q.rss = -1; p[11] = q }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := map[int]processSample{10: {parent: 1, birth: 1, cpu: 1, rss: 1, valid: true}, 11: {parent: 10, birth: 2, cpu: 1, rss: 1, valid: true}}
			after := maps.Clone(before)
			tc.change(after)
			if cpu, memory, ok := treeUsage(10, before, after); ok || cpu != 0 || memory != 0 {
				t.Fatalf("changed scan qualified: %v, %d, %v", cpu, memory, ok)
			}
		})
	}
}

func TestTreeUsageRejectsReapTransferDuringScan(t *testing.T) {
	parentBefore := processSample{parent: 1, birth: 100, cpu: time.Second, reaped: 2 * time.Second, valid: true}
	parentAfter := parentBefore
	parentAfter.reaped = 5 * time.Second
	child := processSample{parent: 10, birth: 101, cpu: 3 * time.Second, valid: true}
	after := map[int]processSample{10: parentAfter}
	for _, tc := range []struct {
		name   string
		before map[int]processSample
	}{
		{"child read before reap, parent after", map[int]processSample{10: parentAfter, 11: child}},
		{"parent read before reap, child already gone", map[int]processSample{10: parentBefore}},
		{"reap between complete scans", map[int]processSample{10: parentBefore, 11: child}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if cpu, memory, ok := treeUsage(10, tc.before, after); ok || cpu != 0 || memory != 0 {
				t.Fatalf("reap transfer qualified: %v, %d, %v", cpu, memory, ok)
			}
		})
	}
}

func TestTreeUsageRejectsOverflow(t *testing.T) {
	for _, p := range []processSample{
		{cpu: math.MaxInt64, reaped: 1, valid: true},
		{rss: math.MaxInt64, valid: true},
		{cpu: -1, valid: true},
		{reaped: -1, valid: true},
		{rss: -1, valid: true},
	} {
		processes := map[int]processSample{10: p, 11: {parent: 10, cpu: 1, rss: 1, valid: true}}
		if cpu, memory, ok := treeUsage(10, processes, processes); ok || cpu != 0 || memory != 0 {
			t.Fatalf("invalid total qualified: %v, %d, %v", cpu, memory, ok)
		}
	}
}

func TestCPUTicks(t *testing.T) {
	for _, tc := range []struct {
		ticks, frequency uint64
		want             time.Duration
		ok               bool
	}{
		{0, 100, 0, true},
		{150, 100, 1500 * time.Millisecond, true},
		{24_000_000, 24_000_000, time.Second, true},
		{math.MaxInt64, 1_000_000_000, math.MaxInt64, true},
		{uint64(math.MaxInt64) + 1, 1_000_000_000, 0, false},
		{math.MaxUint64, 1, 0, false},
		{1, 0, 0, false},
	} {
		got, ok := cpuTicks(tc.ticks, tc.frequency)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%d ticks at %dHz = %v, %v; want %v, %v", tc.ticks, tc.frequency, got, ok, tc.want, tc.ok)
		}
	}
}

func TestTreeUsageRejectsInvalidRoot(t *testing.T) {
	for _, root := range []int{0, -1} {
		p := map[int]processSample{root: {valid: true}}
		if _, _, ok := treeUsage(root, p, p); ok {
			t.Fatalf("invalid root %d qualified", root)
		}
	}
	p := map[int]processSample{10: {parent: 11, valid: true}, 11: {parent: 10, valid: true}}
	if _, _, ok := treeUsage(10, p, p); ok {
		t.Fatal("ancestry cycle qualified")
	}
}

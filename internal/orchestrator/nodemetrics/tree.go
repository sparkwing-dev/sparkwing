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

func processTreeUsage(root int) reading {
	r := reading{at: time.Now()}
	before, ok := processSnapshot(root)
	if !ok {
		return r
	}
	after, ok := processSnapshot(root)
	if !ok {
		return r
	}
	r.cpu, r.memory, r.valid = treeUsage(root, before, after)
	r.processes = treeProcesses(root, before)
	return r
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

func treeProcesses(root int, processes map[int]processSample) map[int]processSample {
	children := make(map[int][]int)
	for pid, p := range processes {
		children[p.parent] = append(children[p.parent], pid)
	}
	selected := make(map[int]processSample)
	queue := []int{root}
	for len(queue) != 0 {
		pid := queue[0]
		queue = queue[1:]
		if _, seen := selected[pid]; seen {
			continue
		}
		p, exists := processes[pid]
		if !exists {
			continue
		}
		selected[pid] = p
		queue = append(queue, children[pid]...)
	}
	return selected
}

func treeUsage(root int, before, after map[int]processSample) (time.Duration, int64, bool) {
	first, last := treeProcesses(root, before), treeProcesses(root, after)
	if root <= 0 || len(first) == 0 || len(first) != len(last) {
		return 0, 0, false
	}
	if _, cycle := first[first[root].parent]; cycle {
		return 0, 0, false
	}
	var cpu, memory int64
	add := func(total *int64, value int64) bool {
		if value < 0 || value > math.MaxInt64-*total {
			return false
		}
		*total += value
		return true
	}
	for pid, p := range first {
		q, exists := last[pid]
		if !exists || !p.valid || !q.valid || p.birth != q.birth || p.parent != q.parent ||
			p.reaped != q.reaped || q.cpu < p.cpu || q.rss < 0 {
			return 0, 0, false
		}
		if !add(&cpu, int64(p.cpu)) || !add(&cpu, int64(p.reaped)) || !add(&memory, p.rss) {
			return 0, 0, false
		}
	}
	return time.Duration(cpu), memory, true
}

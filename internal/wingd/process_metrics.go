package wingd

import (
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type usageMetric struct {
	current, peak, integral, seconds float64
	measured                         bool
	at                               time.Time
}

func (m *usageMetric) observe(value float64, measured bool, now time.Time) {
	if !measured {
		m.measured = false
		m.at = now
		return
	}
	if elapsed := now.Sub(m.at).Seconds(); m.measured && elapsed > 0 {
		m.integral += value * elapsed
		m.seconds += elapsed
	}
	m.current, m.measured, m.at = value, true, now
	if value > m.peak {
		m.peak = value
	}
}

func (m *usageMetric) average() float64 {
	if m.seconds == 0 {
		return m.current
	}
	return m.integral / m.seconds
}

type processMetrics struct {
	heldSince   time.Time
	birth       uint64
	cpu, memory usageMetric
}

func (p *procSampler) recordMetricsLocked(pid int, birth uint64, cpu float64, cpuMeasured bool, memory uint64, memoryMeasured bool, now time.Time) {
	if p.metrics == nil {
		p.metrics = map[int]*processMetrics{}
	}
	m := p.metrics[pid]
	if m == nil || m.birth != birth {
		heldSince := time.Time{}
		if m != nil {
			heldSince = m.heldSince
		}
		m = &processMetrics{birth: birth, heldSince: heldSince}
		p.metrics[pid] = m
	}
	m.cpu.observe(cpu, cpuMeasured, now)
	m.memory.observe(float64(memory), memoryMeasured, now)
}

func (p *procSampler) ProcessUsage(pid int) *wingwire.ProcessUsage {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.measurement != nil {
		return p.measurement.ProcessUsage(pid)
	}
	m := p.metrics[pid]
	if m == nil {
		return nil
	}
	return &wingwire.ProcessUsage{
		CPUMeasured: m.cpu.measured, MemoryMeasured: m.memory.measured,
		CurrentCPUCores: m.cpu.current, PeakCPUCores: m.cpu.peak, AverageCPUCores: m.cpu.average(),
		CurrentMemoryBytes: uint64(m.memory.current), PeakMemoryBytes: uint64(m.memory.peak), AverageMemoryBytes: uint64(m.memory.average()),
	}
}

func (p *procSampler) retainMetrics(roots []OwnedRoot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	live := make(map[int]bool, len(roots))
	if p.metrics == nil {
		p.metrics = map[int]*processMetrics{}
	}
	for _, root := range roots {
		live[root.PID] = true
		if prior := p.metrics[root.PID]; prior == nil || !prior.heldSince.Equal(root.HeldSince) {
			p.metrics[root.PID] = &processMetrics{heldSince: root.HeldSince}
		}
	}
	for pid := range p.metrics {
		if !live[pid] {
			delete(p.metrics, pid)
		}
	}
}

func (d *Daemon) processUsage(pid int) *wingwire.ProcessUsage {
	if sampler, ok := d.procSampler.(interface {
		ProcessUsage(int) *wingwire.ProcessUsage
	}); ok {
		return sampler.ProcessUsage(pid)
	}
	return nil
}

func (d *Daemon) measurementPIDsLocked() map[*conn]int {
	byHolder := map[int]int{}
	bySample := map[int]int{}
	pids := make(map[*conn]int, len(d.byRun))
	for _, c := range d.byRun {
		if c.role != roleHolder {
			continue
		}
		pid := c.samplePID()
		pids[c] = pid
		bySample[pid]++
		byHolder[c.pid]++
	}
	for c, pid := range pids {
		if pid <= 0 || bySample[pid] > 1 || c.measurementPID == 0 && byHolder[c.pid] > 1 {
			delete(pids, c)
		}
	}
	return pids
}

func (d *Daemon) holderProcessUsagesLocked() map[*conn]*wingwire.ProcessUsage {
	usages := make(map[*conn]*wingwire.ProcessUsage, len(d.byRun))
	for c, pid := range d.measurementPIDsLocked() {
		usages[c] = d.processUsage(pid)
	}
	return usages
}

func (d *Daemon) measurementSample() []OwnedRoot {
	d.mu.Lock()
	defer d.mu.Unlock()
	roots := make([]OwnedRoot, 0, len(d.byRun))
	for c, pid := range d.measurementPIDsLocked() {
		roots = append(roots, OwnedRoot{PID: pid, HeldSince: c.startAt})
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].PID < roots[j].PID })
	return roots
}

func (d *Daemon) sampleProcessUsage(roots []OwnedRoot) {
	pids := make([]int, 0, len(roots))
	for _, root := range roots {
		pids = append(pids, root.PID)
	}
	if sampler, ok := d.procSampler.(*procSampler); ok {
		sampler.mu.Lock()
		if sampler.measurement == nil {
			sampler.measurement = newProcSampler()
		}
		measurement := sampler.measurement
		sampler.mu.Unlock()
		measurement.retainMetrics(roots)
		measurement.CPUUsages(pids)
		return
	}
	if sampler, ok := d.procSampler.(procBatchSampler); ok {
		sampler.CPUUsages(pids)
	}
}

func (p *procSampler) unmeasuredMetrics(pids []int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for _, pid := range pids {
		if m := p.metrics[pid]; m != nil {
			m.cpu.observe(0, false, now)
			m.memory.observe(0, false, now)
		}
	}
}

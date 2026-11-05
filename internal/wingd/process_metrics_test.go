package wingd

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

func TestProcessMetrics_TimeWeightedAverageAndPIDReuse(t *testing.T) {
	p := newProcSampler()
	base := time.Unix(100, 0)
	p.recordMetricsLocked(42, 7, 1, true, 100, true, base)
	p.recordMetricsLocked(42, 7, 2, true, 200, true, base.Add(time.Second))
	p.recordMetricsLocked(42, 7, 4, true, 400, true, base.Add(4*time.Second))
	usage := p.ProcessUsage(42)
	if usage.CurrentCPUCores != 4 || usage.PeakCPUCores != 4 || usage.AverageCPUCores != 3.5 {
		t.Fatalf("CPU measurements = %+v", usage)
	}
	if usage.CurrentMemoryBytes != 400 || usage.PeakMemoryBytes != 400 || usage.AverageMemoryBytes != 350 {
		t.Fatalf("memory measurements = %+v", usage)
	}
	p.recordMetricsLocked(42, 8, 0.5, true, 50, true, base.Add(5*time.Second))
	usage = p.ProcessUsage(42)
	if usage.PeakCPUCores != 0.5 || usage.AverageCPUCores != 0.5 || usage.PeakMemoryBytes != 50 {
		t.Fatalf("reused PID retained prior process measurements: %+v", usage)
	}
}

func TestQueueStateIncludesLiveMeasurementsApartFromCharge(t *testing.T) {
	d := newHeadroomDaemon(t, 10, 0)
	if _, _, err := d.ledger.Submit(admission.Request{ID: "job", Cores: 4}); err != nil {
		t.Fatal(err)
	}
	p := newProcSampler()
	p.recordMetricsLocked(42, 7, 1.5, true, 1024, true, time.Unix(100, 0))
	d.procSampler = p
	d.byRun["job"] = &conn{runID: "job", role: roleHolder, pid: 42}
	qs := d.readQueueState()
	if len(qs.Holders) != 1 || qs.Holders[0].Usage == nil {
		t.Fatalf("holder live measurements missing: %+v", qs.Holders)
	}
	h := qs.Holders[0]
	if h.Resources.Cores != 4 || h.Usage.CurrentCPUCores != 1.5 || h.Usage.CurrentMemoryBytes != 1024 {
		t.Fatalf("charge and measurement = %+v / %+v", h.Resources, h.Usage)
	}
}

func TestProcessMetrics_NewHoldStartsFreshForTheSameProcess(t *testing.T) {
	p := newProcSampler()
	base := time.Unix(100, 0)
	p.retainMetrics([]OwnedRoot{{PID: 42, HeldSince: base}})
	p.recordMetricsLocked(42, 7, 4, true, 400, true, base)
	p.retainMetrics([]OwnedRoot{{PID: 42, HeldSince: base.Add(time.Second)}})
	p.recordMetricsLocked(42, 7, 1, true, 100, true, base.Add(time.Second))
	usage := p.ProcessUsage(42)
	if usage.PeakCPUCores != 1 || usage.PeakMemoryBytes != 100 {
		t.Fatalf("new holder retained prior holder's peaks: %+v", usage)
	}
}

func TestProcessMetrics_UnreadableIntervalsDoNotChangeAverage(t *testing.T) {
	p := newProcSampler()
	base := time.Unix(100, 0)
	p.recordMetricsLocked(42, 7, 1, true, 100, true, base)
	p.recordMetricsLocked(42, 7, 2, true, 200, true, base.Add(time.Second))
	p.recordMetricsLocked(42, 7, 0, false, 0, false, base.Add(10*time.Second))
	if usage := p.ProcessUsage(42); usage.CPUMeasured || usage.MemoryMeasured {
		t.Fatalf("unreadable process reported measured: %+v", usage)
	}
	p.recordMetricsLocked(42, 7, 3, true, 300, true, base.Add(20*time.Second))
	usage := p.ProcessUsage(42)
	if usage.AverageCPUCores != 2 || usage.AverageMemoryBytes != 200 {
		t.Fatalf("unreadable interval changed averages: %+v", usage)
	}
	p.retainMetrics(nil)
	if usage := p.ProcessUsage(42); usage != nil {
		t.Fatalf("released process retained measurements: %+v", usage)
	}
}

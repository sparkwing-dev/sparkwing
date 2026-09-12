package wingd

import (
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func attributionHost(totalCores, busy float64) HostStat {
	return HostStat{
		TotalCores:       totalCores,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		BusyCores:        busy,
		LoadAverage:      busy,
		LoadMeasured:     true,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	}
}

func refreshWithCohortChange(t *testing.T, d *Daemon, owned float64, mutate func()) {
	t.Helper()
	blocking := &blockingOwnedCPUSampler{
		started:  make(chan []int, 1),
		release:  make(chan struct{}),
		fraction: owned,
	}
	d.ownedSampler = blocking
	done := make(chan struct{})
	go func() {
		d.refreshHeadroom()
		close(done)
	}()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("owned CPU sampling did not start")
	}
	d.mu.Lock()
	mutate()
	d.mu.Unlock()
	close(blocking.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("headroom refresh did not finish")
	}
}

func newAttributedDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := newHeadroomDaemon(t, 10, 0.2)
	d.sampler = &countingHostSampler{stat: attributionHost(10, 8.5)}
	d.ownedSampler = &fixedOwnedCPUSampler{fraction: 6.5, measured: true}
	d.byRun["holder"] = &conn{runID: "holder", role: roleHolder, pid: 4242}
	return d
}

func TestRefreshHeadroom_ChargesOnlyUnownedCPUAsExternal(t *testing.T) {
	d := newAttributedDaemon(t)

	d.refreshHeadroom()

	cores := queueRow(t, queueState(t, d), "cores")
	if math.Abs(cores.External-2) > coresEpsilon {
		t.Errorf("external cores = %.2f, want 2.00: 8.5 busy less the 6.5 this daemon's holders ran",
			cores.External)
	}
	if math.Abs(d.appliedCores-6) > coresEpsilon {
		t.Errorf("grantable cores = %.2f, want 6.00: 10 total less the 2.0 reserve and 2.0 external",
			d.appliedCores)
	}
}

func TestRefreshHeadroom_CohortChangeKeepsTheOwnedAttribution(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Daemon)
	}{
		{"holder starts", func(d *Daemon) {
			d.byRun["second"] = &conn{runID: "second", role: roleHolder, pid: 5150}
		}},
		{"holder finishes", func(d *Daemon) { delete(d.byRun, "holder") }},
		{"holder root pid moves", func(d *Daemon) { d.byRun["holder"].pid = 5150 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newAttributedDaemon(t)
			d.refreshHeadroom()

			refreshWithCohortChange(t, d, 6.5, func() { tc.mutate(d) })

			if math.Abs(d.smoothedExternal-2) > coresEpsilon {
				t.Errorf("smoothed external = %.2f, want 2.00: the holder set moved, the machine did not",
					d.smoothedExternal)
			}
			if math.Abs(d.appliedCores-6) > coresEpsilon {
				t.Errorf("grantable cores = %.2f, want 6.00: a moving holder set must not spend this daemon's budget",
					d.appliedCores)
			}
		})
	}
}

func TestRefreshHeadroom_SustainedCohortChangeLeavesSomethingGrantable(t *testing.T) {
	d := newAttributedDaemon(t)
	d.refreshHeadroom()

	for i := range 8 {
		pid := 5150 + i
		refreshWithCohortChange(t, d, 6.5, func() { d.byRun["holder"].pid = pid })
	}

	if d.appliedCores <= 0 {
		t.Fatalf("grantable cores = %.2f, want more than zero: a busy holder set must not spend this daemon's whole budget",
			d.appliedCores)
	}
	if math.Abs(d.appliedCores-6) > coresEpsilon {
		t.Errorf("grantable cores = %.2f, want 6.00: every sample measured the same 6.5 of owned work",
			d.appliedCores)
	}
}

func TestRefreshHeadroom_KeptOwnedReadingExpires(t *testing.T) {
	now := time.Now()
	d := newAttributedDaemon(t)
	d.cfg.Now = func() time.Time { return now }
	d.refreshHeadroom()

	now = now.Add(d.cfg.headroomMaxAge() + time.Second)
	refreshWithCohortChange(t, d, 6.5, func() { delete(d.byRun, "holder") })

	if math.Abs(d.smoothedExternal-4.6) > coresEpsilon {
		t.Errorf("smoothed external = %.2f, want 4.60: a reading older than the headroom age is spent, so the sample charges the whole 8.5 busy",
			d.smoothedExternal)
	}
}

func TestRefreshHeadroom_CountsOwnedAttributionOutcomes(t *testing.T) {
	d := newAttributedDaemon(t)
	d.refreshHeadroom()

	if got := queueAttribution(t, d); got.CohortChanged != 0 || got.Retained != 0 || got.Unattributed != 0 {
		t.Fatalf("attribution after a clean sample = %+v, want every counter at zero", got)
	}

	refreshWithCohortChange(t, d, 6.5, func() { d.byRun["holder"].pid = 5150 })

	got := queueAttribution(t, d)
	if got.CohortChanged != 1 {
		t.Errorf("cohort-changed count = %d, want 1", got.CohortChanged)
	}
	if got.Retained != 1 {
		t.Errorf("retained count = %d, want 1: the previous reading covered the sample", got.Retained)
	}
	if got.Unattributed != 0 {
		t.Errorf("unattributed count = %d, want 0: nothing was charged unattributed", got.Unattributed)
	}
}

func TestRefreshHeadroom_CountsAnUnreadableOwnedSampler(t *testing.T) {
	d := newAttributedDaemon(t)
	d.ownedSampler = &fixedOwnedCPUSampler{measured: false}

	d.refreshHeadroom()

	got := queueAttribution(t, d)
	if got.Unattributed != 1 {
		t.Errorf("unattributed count = %d, want 1: the owned sampler read nothing", got.Unattributed)
	}
	if got.CohortChanged != 0 {
		t.Errorf("cohort-changed count = %d, want 0: the holder set held still", got.CohortChanged)
	}
}

func queueAttribution(t *testing.T, d *Daemon) wingwire.ExternalAttribution {
	t.Helper()
	if a := queueState(t, d).ExternalAttribution; a != nil {
		return *a
	}
	return wingwire.ExternalAttribution{}
}

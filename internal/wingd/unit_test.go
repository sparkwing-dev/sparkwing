package wingd

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestChargedResources(t *testing.T) {
	tests := []struct {
		name string
		in   wingwire.HostResources
		want wingwire.HostResources
	}{
		{"unhinted charges default core", wingwire.HostResources{}, wingwire.HostResources{Cores: defaultChargeCores}},
		{"declared cores pass through", wingwire.HostResources{Cores: 2}, wingwire.HostResources{Cores: 2}},
		{"declared memory alone passes through", wingwire.HostResources{MemoryBytes: 100}, wingwire.HostResources{MemoryBytes: 100}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chargedResources(tt.in); got != tt.want {
				t.Fatalf("chargedResources(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRequestFromWire(t *testing.T) {
	req := requestFromWire("r1", wingwire.HostResources{Cores: 1.5, MemoryBytes: 2048},
		[]wingwire.SemaphoreClaim{{Name: "k", Capacity: 3, Cost: 2, Policy: wingwire.PolicyCancelOthers}})
	if req.ID != "r1" || req.Cores != 1.5 || req.MemoryBytes != 2048 {
		t.Fatalf("host fields wrong: %+v", req)
	}
	if len(req.Semaphores) != 1 {
		t.Fatalf("want 1 semaphore, got %d", len(req.Semaphores))
	}
	s := req.Semaphores[0]
	if s.Key != "k" || s.Capacity != 3 || s.Cost != 2 || s.Policy != admission.PolicyCancelOthers {
		t.Fatalf("semaphore mapped wrong: %+v", s)
	}
}

func TestRequestFromWaiter_RoundTrips(t *testing.T) {
	w := admission.WaiterState{
		RequestID:   "w",
		MilliCores:  2500,
		MemoryBytes: 4096,
		Claims:      []admission.ClaimState{{Key: "k", Capacity: 2, Cost: 1, Policy: admission.PolicyQueue}},
	}
	req := requestFromWaiter(w)
	if req.ID != "w" || req.Cores != 2.5 || req.MemoryBytes != 4096 {
		t.Fatalf("host fields wrong: %+v", req)
	}
	if len(req.Semaphores) != 1 || req.Semaphores[0].Key != "k" {
		t.Fatalf("claims wrong: %+v", req.Semaphores)
	}
}

func TestSubmitErrorKey(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{admission.ErrNeverAdmissible, "never_admissible"},
		{admission.ErrDuplicateID, "duplicate"},
		{admission.ErrInvalidRequest, "invalid"},
	}
	for _, tt := range tests {
		if got := submitErrorKey(tt.err); got != tt.want {
			t.Fatalf("submitErrorKey(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

type fixedHostSampler struct {
	stat HostStat
}

func (s fixedHostSampler) Sample() (HostStat, error) {
	return s.stat, nil
}

func TestInitLedger_ResizesRestoredTotalsToCurrentBudget(t *testing.T) {
	home := t.TempDir()
	original, err := admission.New(admission.Config{TotalCores: 8, TotalMemoryBytes: 2048})
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	dec, _, err := original.Submit(admission.Request{ID: "holder", Cores: 1, MemoryBytes: 512})
	if err != nil {
		t.Fatalf("submit holder: %v", err)
	}
	if dec.Kind != admission.DecisionGranted {
		t.Fatalf("holder = %s, want %s", dec.Kind, admission.DecisionGranted)
	}

	d, err := New(Config{
		Home: home,
		Sampler: fixedHostSampler{stat: HostStat{
			TotalCores:       2,
			TotalMemoryBytes: 1024,
			FreeMemoryBytes:  1024,
		}},
	})
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}
	if err := d.layout.ensureDir(); err != nil {
		t.Fatalf("ensure dir: %v", err)
	}
	if err := writeState(d.layout.state, original.Snapshot(), nil); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := d.initLedger(); err != nil {
		t.Fatalf("init ledger: %v", err)
	}

	dec, _, err = d.ledger.Submit(admission.Request{ID: "waiter", Cores: 2, MemoryBytes: 1024})
	if err != nil {
		t.Fatalf("submit waiter: %v", err)
	}
	if dec.Kind != admission.DecisionQueued {
		t.Fatalf("restored ledger admitted against stale totals: got %s, want %s", dec.Kind, admission.DecisionQueued)
	}
}

// newHeadroomDaemon builds a daemon with a ready ledger but no listener,
// for exercising the headroom controller in isolation.
func newHeadroomDaemon(t *testing.T, totalCores float64, frac float64) *Daemon {
	t.Helper()
	home := t.TempDir()
	d, err := New(Config{Home: home, HeadroomFraction: frac})
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}
	if err := d.layout.ensureDir(); err != nil {
		t.Fatalf("ensure dir: %v", err)
	}
	lg, err := admission.New(admission.Config{TotalCores: totalCores, TotalMemoryBytes: 16 << 30})
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	d.ledger = lg
	return d
}

func TestApplyHeadroom_GatesUnderLoad(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	dec, _, err := d.ledger.Submit(admission.Request{ID: "holder", Cores: 1})
	if err != nil {
		t.Fatalf("submit holder: %v", err)
	}
	if dec.Kind != admission.DecisionGranted {
		t.Fatalf("initial holder = %s, want %s", dec.Kind, admission.DecisionGranted)
	}

	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 7.5, FreeMemoryBytes: 16 << 30})

	dec, _, err = d.ledger.Submit(admission.Request{ID: "big", Cores: 2})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if dec.Kind != admission.DecisionQueued {
		t.Fatalf("under high load a 2-core request should queue, got %s", dec.Kind)
	}
}

func TestApplyHeadroom_AdmitsWithHeadroom(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 0, FreeMemoryBytes: 16 << 30})

	dec, _, err := d.ledger.Submit(admission.Request{ID: "ok", Cores: 2})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if dec.Kind != admission.DecisionGranted {
		t.Fatalf("with headroom a 2-core request should be granted, got %s", dec.Kind)
	}
}

func TestApplyHeadroom_IgnoreExternalAdmitsUnderLoad(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.cfg.Budget = Budget{IgnoreExternal: true}
	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 7.5, FreeMemoryBytes: 16 << 30})

	dec, _, err := d.ledger.Submit(admission.Request{ID: "ok", Cores: 2})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if dec.Kind != admission.DecisionGranted {
		t.Fatalf("with ignore-external a 2-core request should be granted under load, got %s", dec.Kind)
	}
	if d.externalCores < 7.0 {
		t.Errorf("externalCores = %.2f, want the real ~7.5 reading kept for observability", d.externalCores)
	}
}

// TestApplyHeadroom_IgnoreExternalStillDetectsSaturation pins that
// ignore-external only relaxes admission: contention accounting keeps
// folding the real saturation into a holder, so observability stays
// truthful while admission stops subtracting external load.
func TestApplyHeadroom_IgnoreExternalStillDetectsSaturation(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.cfg.Budget = Budget{IgnoreExternal: true}
	holder := &conn{role: roleHolder, finalizable: true}
	d.conns[holder] = struct{}{}

	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 7.5, FreeMemoryBytes: 16 << 30})

	if holder.holdSampledMS <= 0 {
		t.Fatalf("holder should have accrued a sampled interval, got %d", holder.holdSampledMS)
	}
	if holder.holdSaturatedMS <= 0 {
		t.Errorf("ignore-external must not blind contention: holder saturated time = %d, want > 0", holder.holdSaturatedMS)
	}

	dec, _, err := d.ledger.Submit(admission.Request{ID: "ok", Cores: 2})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if dec.Kind != admission.DecisionGranted {
		t.Fatalf("admission should ignore external and grant, got %s", dec.Kind)
	}
}

func TestApplyHeadroom_Hysteresis(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 0, FreeMemoryBytes: 16 << 30})
	first := d.appliedCores
	if !d.headroomInit {
		t.Fatal("headroom should be initialized after first apply")
	}
	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 0.1, FreeMemoryBytes: 16 << 30})
	if d.appliedCores != first {
		t.Fatalf("a tiny load change (%v -> %v) should not move headroom past the deadband", first, d.appliedCores)
	}
}

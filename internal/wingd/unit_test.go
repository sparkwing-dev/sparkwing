package wingd

import (
	"errors"
	"net"
	"testing"
	"time"

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
		[]wingwire.SemaphoreClaim{{Name: "k", Capacity: 3, Cost: 2, Policy: wingwire.PolicyCancelOthers}}, "", 7)
	if req.ID != "r1" || req.Cores != 1.5 || req.MemoryBytes != 2048 {
		t.Fatalf("host fields wrong: %+v", req)
	}
	if req.Priority != 7 {
		t.Fatalf("priority = %d, want 7", req.Priority)
	}
	if req.SoftCores {
		t.Fatalf("explicit core request should stay hard: %+v", req)
	}
	if req.StrictCores {
		t.Fatalf("unknown cost source should not be a strict pin: %+v", req)
	}
	if len(req.Semaphores) != 1 {
		t.Fatalf("want 1 semaphore, got %d", len(req.Semaphores))
	}
	s := req.Semaphores[0]
	if s.Key != "k" || s.Capacity != 3 || s.Cost != 2 || s.Policy != admission.PolicyCancelOthers {
		t.Fatalf("semaphore mapped wrong: %+v", s)
	}
}

func TestRequestFromWire_ProfiledCoresAreSoft(t *testing.T) {
	autoMeasured := []wingwire.CostSource{
		wingwire.CostSourceMeasured, wingwire.CostSourceDefault,
		wingwire.CostSourceMeasuring, wingwire.CostSourceFloor,
	}
	for _, source := range autoMeasured {
		req := requestFromWire("r1", wingwire.HostResources{Cores: 1.5}, nil, source, 0)
		if !req.SoftCores {
			t.Fatalf("%s core request should use CPU as backpressure", source)
		}
		if req.StrictCores {
			t.Fatalf("%s core request should not be a strict pin", source)
		}
	}
	req := requestFromWire("r1", wingwire.HostResources{Cores: 1.5}, nil, wingwire.CostSourcePin, 0)
	if req.SoftCores || !req.StrictCores {
		t.Fatalf("pinned core request = soft %v strict %v, want hard strict", req.SoftCores, req.StrictCores)
	}
}

func TestValidCostSource_AcceptsEveryResolvedSource(t *testing.T) {
	valid := []wingwire.CostSource{
		"", wingwire.CostSourcePin, wingwire.CostSourceMeasured, wingwire.CostSourceDefault,
		wingwire.CostSourceMeasuring, wingwire.CostSourceFloor,
	}
	for _, source := range valid {
		if !validCostSource(source) {
			t.Errorf("validCostSource(%q) = false, want true", source)
		}
	}
	if validCostSource(wingwire.CostSource("typo")) {
		t.Error("validCostSource(\"typo\") = true, want false")
	}
}

func TestRequestFromWaiter_RoundTrips(t *testing.T) {
	w := admission.WaiterState{
		RequestID:   "w",
		Priority:    9,
		MilliCores:  2500,
		SoftCores:   true,
		StrictCores: true,
		MemoryBytes: 4096,
		Claims:      []admission.ClaimState{{Key: "k", Capacity: 2, Cost: 1, Policy: admission.PolicyQueue}},
	}
	req := requestFromWaiter(w)
	if req.ID != "w" || req.Cores != 2.5 || !req.SoftCores || !req.StrictCores || req.MemoryBytes != 4096 {
		t.Fatalf("host fields wrong: %+v", req)
	}
	if req.Priority != 9 {
		t.Fatalf("priority = %d, want 9", req.Priority)
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

type hostSamplerFunc func() (HostStat, error)

func (f hostSamplerFunc) Sample() (HostStat, error) { return f() }

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
			LoadMeasured:     true,
			MemoryMeasured:   true,
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
func newHeadroomDaemon(t *testing.T, totalCores, frac float64) *Daemon {
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

	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 7.5, FreeMemoryBytes: 16 << 30, LoadMeasured: true, MemoryMeasured: true})

	dec, _, err = d.ledger.Submit(admission.Request{ID: "big", Cores: 2})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if dec.Kind != admission.DecisionQueued {
		t.Fatalf("under high load a 2-core request behind a holder should queue, got %s", dec.Kind)
	}
}

func TestApplyHeadroom_AdmitsWithHeadroom(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 0, FreeMemoryBytes: 16 << 30, LoadMeasured: true, MemoryMeasured: true})

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
	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 7.5, FreeMemoryBytes: 16 << 30, LoadMeasured: true, MemoryMeasured: true})

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

	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 7.5, FreeMemoryBytes: 16 << 30, LoadMeasured: true, MemoryMeasured: true})

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
	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 0, FreeMemoryBytes: 16 << 30, LoadMeasured: true, MemoryMeasured: true})
	first := d.appliedCores
	if !d.headroomInit {
		t.Fatal("headroom should be initialized after first apply")
	}
	d.applyHeadroom(HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, LoadAverage: 0.1, FreeMemoryBytes: 16 << 30, LoadMeasured: true, MemoryMeasured: true})
	if d.appliedCores != first {
		t.Fatalf("a tiny load change (%v -> %v) should not move headroom past the deadband", first, d.appliedCores)
	}
}

func TestApplyHeadroom_BoundedRefreshAdmitsSoleMemoryWaiter(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	now := time.Unix(1_700_000_000, 0)
	d.cfg.Now = func() time.Time { return now }
	d.cfg.HeadroomMaxAge = 30 * time.Second
	high := HostStat{
		TotalCores:       8,
		TotalMemoryBytes: 16 << 30,
		FreeMemoryBytes:  2 << 30,
		LoadMeasured:     true,
		MemoryMeasured:   true,
	}
	d.applyHeadroom(high)
	holder, _, err := d.ledger.Submit(admission.Request{ID: "holder", Cores: 0.1})
	if err != nil || holder.Kind != admission.DecisionGranted {
		t.Fatalf("submit holder = (%s, %v), want granted", holder.Kind, err)
	}

	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	waiter := newConn(d, server)
	waiter.runID = "waiter"
	waiter.role = roleWaiter
	waiter.resources = wingwire.HostResources{MemoryBytes: 512 << 20}
	d.byRun[waiter.runID] = waiter
	dec, _, err := d.ledger.Submit(admission.Request{ID: waiter.runID, MemoryBytes: 512 << 20})
	if err != nil {
		t.Fatalf("submit waiter: %v", err)
	}
	if dec.Kind != admission.DecisionQueued {
		t.Fatalf("high external memory decision = %s, want %s", dec.Kind, admission.DecisionQueued)
	}

	recovered := high
	recovered.FreeMemoryBytes = 4065119436
	now = now.Add(20 * time.Second)
	d.applyHeadroom(recovered)
	if waiter.role != roleWaiter {
		t.Fatalf("sub-deadband recovery admitted before freshness bound: role = %d", waiter.role)
	}

	read := make(chan wingwire.Message, 1)
	go func() {
		msg, _ := newConn(d, peer).readMessage()
		read <- msg
	}()
	now = now.Add(10 * time.Second)
	d.applyHeadroom(recovered)
	select {
	case msg := <-read:
		grant, ok := msg.(*wingwire.Grant)
		if !ok || grant.RunID != waiter.runID {
			t.Fatalf("bounded refresh delivery = %#v, want grant for %q", msg, waiter.runID)
		}
	case <-time.After(time.Second):
		t.Fatal("sole waiter was not admitted at the freshness bound")
	}
}

func TestApplyHeadroom_MeasurementAndEffectiveAgesAreDistinct(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	now := time.Unix(1_700_000_000, 0)
	d.cfg.Now = func() time.Time { return now }
	d.cfg.HeadroomMaxAge = time.Minute
	stat := HostStat{
		TotalCores:       8,
		TotalMemoryBytes: 16 << 30,
		FreeMemoryBytes:  16 << 30,
		LoadAverage:      1,
		LoadMeasured:     true,
		MemoryMeasured:   true,
	}
	d.applyHeadroom(stat)
	now = now.Add(10 * time.Second)
	stat.LoadAverage = 1.1
	d.applyHeadroom(stat)
	now = now.Add(5 * time.Second)

	qs := queueState(t, d)
	if qs.ExternalSampleAgeMS != 15000 {
		t.Fatalf("effective age = %dms, want 15000ms", qs.ExternalSampleAgeMS)
	}
	if qs.ExternalMeasurementAgeMS != 5000 {
		t.Fatalf("measurement age = %dms, want 5000ms", qs.ExternalMeasurementAgeMS)
	}
}

func TestApplyHeadroom_UnmeasuredStatDoesNotRefreshMeasurementAge(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	now := time.Unix(1_700_000_000, 0)
	d.cfg.Now = func() time.Time { return now }
	stat := HostStat{
		TotalCores:       8,
		TotalMemoryBytes: 16 << 30,
		FreeMemoryBytes:  16 << 30,
		LoadAverage:      1,
		LoadMeasured:     true,
		MemoryMeasured:   true,
	}
	d.applyHeadroom(stat)
	measuredAt := d.measuredAt

	now = now.Add(10 * time.Second)
	stat.LoadMeasured = false
	stat.MemoryMeasured = false
	d.applyHeadroom(stat)
	if d.measuredAt != measuredAt {
		t.Fatalf("unmeasured stat advanced measurement time from %v to %v", measuredAt, d.measuredAt)
	}
	if d.headroomAt != now {
		t.Fatal("unmeasured state did not become the effective admission state")
	}
}

func TestApplyHeadroom_DeadbandDoesNotFlapBeforeBound(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	now := time.Unix(1_700_000_000, 0)
	d.cfg.Now = func() time.Time { return now }
	d.cfg.HeadroomMaxAge = 30 * time.Second
	stat := HostStat{
		TotalCores:       8,
		TotalMemoryBytes: 16 << 30,
		FreeMemoryBytes:  16 << 30,
		LoadAverage:      1,
		LoadMeasured:     true,
		MemoryMeasured:   true,
	}
	d.applyHeadroom(stat)
	firstAt := d.headroomAt
	firstCores := d.appliedCores

	for i, load := range []float64{1.1, 0.9, 1.15, 0.85, 1.05} {
		now = now.Add(5 * time.Second)
		stat.LoadAverage = load
		d.applyHeadroom(stat)
		if d.headroomAt != firstAt || d.appliedCores != firstCores {
			t.Fatalf("sample %d escaped deadband before bound: at=%v cores=%v", i, d.headroomAt, d.appliedCores)
		}
	}

	now = now.Add(5 * time.Second)
	d.applyHeadroom(stat)
	if d.headroomAt != now {
		t.Fatalf("effective timestamp = %v, want bounded refresh at %v", d.headroomAt, now)
	}
	refreshedAt := d.headroomAt
	now = now.Add(5 * time.Second)
	stat.LoadAverage = 0.9
	d.applyHeadroom(stat)
	if d.headroomAt != refreshedAt {
		t.Fatal("bounded refresh disabled the deadband on the following sample")
	}
}

func TestRefreshHeadroom_FailedSampleDoesNotRefreshOrAdmit(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	now := time.Unix(1_700_000_000, 0)
	d.cfg.Now = func() time.Time { return now }
	d.cfg.HeadroomMaxAge = 30 * time.Second
	high := HostStat{
		TotalCores:       8,
		TotalMemoryBytes: 16 << 30,
		FreeMemoryBytes:  2 << 30,
		LoadMeasured:     true,
		MemoryMeasured:   true,
	}
	d.applyHeadroom(high)
	holder, _, err := d.ledger.Submit(admission.Request{ID: "holder", Cores: 0.1})
	if err != nil || holder.Kind != admission.DecisionGranted {
		t.Fatalf("submit holder = (%s, %v), want granted", holder.Kind, err)
	}
	measuredAt, effectiveAt := d.measuredAt, d.headroomAt
	dec, _, err := d.ledger.Submit(admission.Request{ID: "waiter", MemoryBytes: 512 << 20})
	if err != nil || dec.Kind != admission.DecisionQueued {
		t.Fatalf("submit waiter = (%s, %v), want queued", dec.Kind, err)
	}

	now = now.Add(31 * time.Second)
	d.sampler = hostSamplerFunc(func() (HostStat, error) {
		idle := high
		idle.FreeMemoryBytes = idle.TotalMemoryBytes
		return idle, errors.New("sensor unavailable")
	})
	d.refreshHeadroom()
	if d.measuredAt != measuredAt || d.headroomAt != effectiveAt {
		t.Fatalf("failed sample advanced timestamps: measured=%v effective=%v", d.measuredAt, d.headroomAt)
	}
	snap := d.ledger.Snapshot()
	if len(snap.Waiters) != 1 || snap.Waiters[0].RequestID != "waiter" {
		t.Fatalf("errored idle-looking sample admitted waiter: %+v", snap.Waiters)
	}
}

// TestClampHostChargeLocked_CapsMemoryLikeCores is the daemon-side backstop
// for a charge resolved with no daemon answering: both host dimensions cap at
// what the box grants a single run, so a still-measuring pipeline can never
// submit a demand the machine will refuse forever. An explicit pin stays hard
// on both dimensions, since a pin is the operator saying they mean it.
func TestClampHostChargeLocked_CapsMemoryLikeCores(t *testing.T) {
	d := &Daemon{
		cfg:           Config{HeadroomFraction: 0.1},
		machineCores:  8,
		machineMemory: 16 << 30,
	}

	got, _ := d.clampHostChargeLocked(
		wingwire.HostResources{Cores: 20, MemoryBytes: 32 << 30}, wingwire.CostSourceFloor)
	if got.Cores != 7.2 {
		t.Errorf("cores = %v, want the grantable 7.2", got.Cores)
	}
	total := uint64(16 << 30)
	if want := int64(uint64(float64(total) * 0.9)); got.MemoryBytes != want {
		t.Errorf("memory = %d, want the grantable %d", got.MemoryBytes, want)
	}

	pinned, _ := d.clampHostChargeLocked(
		wingwire.HostResources{Cores: 20, MemoryBytes: 32 << 30}, wingwire.CostSourcePin)
	if pinned.Cores != 20 || pinned.MemoryBytes != 32<<30 {
		t.Errorf("pin clamped to %+v, want it left hard", pinned)
	}
}

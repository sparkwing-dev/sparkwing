package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func capacityBackend(t *testing.T) (*store.Store, backend.Backend) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, backend.NewStoreBackend(st, paths.Paths{Root: dir}, nil)
}

func measure(t *testing.T, st *store.Store, pipeline string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if err := st.RecordProfileObservation(context.Background(), pipeline, "", store.ProfileObservation{
			Duration:        time.Duration(i) * time.Second,
			PeakCores:       float64(i) * 2,
			SustainedCores:  float64(i),
			PeakMemoryBytes: int64(i) << 20,
			CPUMeasured:     true,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func getJSON[T any](t *testing.T, h http.HandlerFunc, target string, want int) T {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, want, rec.Body.String())
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return out
}

func TestCapacityProfiles_ChargesMeasuredSustainedCores(t *testing.T) {
	st, b := capacityBackend(t)
	measure(t, st, "demo", 10)

	body := getJSON[capacityProfilesPayload](t,
		capacityProfilesHandler(b), "/api/v1/capacity/profiles", http.StatusOK)

	if len(body.Profiles) != 1 {
		t.Fatalf("profiles = %d, want 1: %+v", len(body.Profiles), body.Profiles)
	}
	p := body.Profiles[0]
	if p.Pipeline != "demo" {
		t.Errorf("pipeline = %q", p.Pipeline)
	}
	if p.SampleCount != 10 {
		t.Errorf("sample_count = %d, want 10", p.SampleCount)
	}
	if p.Charge.Source != string(store.CostSourceMeasured) {
		t.Errorf("source = %q, want measured", p.Charge.Source)
	}
	if p.Charge.CoresBasis != "sustained_p95" {
		t.Errorf("cores_basis = %q, want sustained_p95", p.Charge.CoresBasis)
	}
	if p.Charge.Cores != p.SustainedCores {
		t.Errorf("charge cores = %v, sustained = %v", p.Charge.Cores, p.SustainedCores)
	}
	if p.Charge.MemoryBytes != p.PeakMemoryBytes {
		t.Errorf("charge memory = %d, peak memory = %d", p.Charge.MemoryBytes, p.PeakMemoryBytes)
	}
	if p.Charge.Rationale != "measured sustained p95 over 10 runs" {
		t.Errorf("rationale = %q", p.Charge.Rationale)
	}
	if body.MachineCores <= 0 || body.Constants.ColdStartCores <= 0 {
		t.Errorf("machine context missing: cores=%d constants=%+v", body.MachineCores, body.Constants)
	}
}

func TestCapacityProfiles_ColdStartBeforeEnoughSamples(t *testing.T) {
	st, b := capacityBackend(t)
	measure(t, st, "fresh", 1)

	body := getJSON[capacityProfilesPayload](t,
		capacityProfilesHandler(b), "/api/v1/capacity/profiles", http.StatusOK)

	p := body.Profiles[0]
	if p.Charge.Source != string(store.CostSourceDefault) {
		t.Fatalf("source = %q, want default under %d samples", p.Charge.Source, body.Constants.MinSamples)
	}
	if p.Charge.Cores != body.Constants.ColdStartCores {
		t.Errorf("charge = %v, cold start = %v", p.Charge.Cores, body.Constants.ColdStartCores)
	}
}

func TestCapacityProfiles_PinWinsAndDriftIsReported(t *testing.T) {
	st, b := capacityBackend(t)
	measure(t, st, "pinned", 10)
	if err := st.SetProfilePin(context.Background(), "pinned", "", 16, 0); err != nil {
		t.Fatal(err)
	}

	body := getJSON[capacityProfilesPayload](t,
		capacityProfilesHandler(b), "/api/v1/capacity/profiles", http.StatusOK)

	p := body.Profiles[0]
	if p.Charge.Source != string(store.CostSourcePin) || p.Charge.Cores != 16 {
		t.Fatalf("charge = %+v, want the pin verbatim", p.Charge)
	}
	if p.Drift == "" || p.DriftClass == "" {
		t.Errorf("a 16-core pin over a ~10-core measurement should report drift: %+v", p)
	}
}

func TestCapacityProfiles_UnsupportedBackend(t *testing.T) {
	rec := httptest.NewRecorder()
	capacityProfilesHandler(&fakeBackend{})(rec, httptest.NewRequest(http.MethodGet, "/api/v1/capacity/profiles", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body = %s", rec.Code, rec.Body.String())
	}
}

func TestCapacityExplain_MarksTheSampleThePriceCameFrom(t *testing.T) {
	st, b := capacityBackend(t)
	measure(t, st, "demo", 10)

	body := getJSON[capacityExplainPayload](t, capacityExplainHandler(b),
		"/api/v1/capacity/profiles/explain?pipeline=demo", http.StatusOK)

	if len(body.Samples) != 10 {
		t.Fatalf("samples = %d, want the stored window", len(body.Samples))
	}
	sel := body.Selections.Cores
	if sel.Field != "sustained_cores" {
		t.Errorf("cores selection field = %q", sel.Field)
	}
	if sel.Count != 10 || sel.Rank != 10 {
		t.Errorf("rank %d of %d, want p95 of 10 samples at rank 10", sel.Rank, sel.Count)
	}
	if sel.Index < 0 || sel.Index >= len(body.Samples) {
		t.Fatalf("selected index %d is outside the window", sel.Index)
	}
	if got := body.Samples[sel.Index].SustainedCores; got != sel.Value {
		t.Errorf("marked sample has sustained %v, selection value %v", got, sel.Value)
	}
	if !sel.Matches || sel.Value != body.Profile.SustainedCores {
		t.Errorf("recomputed %v does not reproduce stored %v", sel.Value, body.Profile.SustainedCores)
	}
	mem := body.Selections.Memory
	if !mem.Matches || int64(mem.Value) != body.Profile.PeakMemoryBytes {
		t.Errorf("memory selection %+v does not reproduce stored %d", mem, body.Profile.PeakMemoryBytes)
	}
	if !body.Selections.DurationP50.Matches || !body.Selections.DurationP99.Matches {
		t.Errorf("duration selections do not reproduce the stored percentiles: %+v", body.Selections)
	}
}

func TestCapacityExplain_ChainMarksExactlyOneResolvedStep(t *testing.T) {
	st, b := capacityBackend(t)
	measure(t, st, "demo", 10)

	body := getJSON[capacityExplainPayload](t, capacityExplainHandler(b),
		"/api/v1/capacity/profiles/explain?pipeline=demo", http.StatusOK)

	applied := []string{}
	for _, s := range body.Chain {
		if s.Applied {
			applied = append(applied, s.Step)
		}
	}
	if len(applied) != 1 || applied[0] != "measured" {
		t.Fatalf("applied steps = %v, want exactly the measured rung", applied)
	}
	steps := map[string]chargeStep{}
	for _, s := range body.Chain {
		steps[s.Step] = s
	}
	if steps["pin"].Eligible {
		t.Error("unpinned pipeline reports an eligible pin rung")
	}
	if !steps["cold_start"].Eligible || steps["cold_start"].Cores != body.Constants.ColdStartCores {
		t.Errorf("cold start rung = %+v, want the machine's cold-start charge", steps["cold_start"])
	}
	if body.CeilingNote == "" {
		t.Error("the charge is shown without the admission-time ceiling caveat")
	}
}

func TestCapacityExplain_NodeRowsAccompanyTheRollup(t *testing.T) {
	st, b := capacityBackend(t)
	measure(t, st, "demo", 4)
	if err := st.RecordProfileObservation(context.Background(), "demo", "build", store.ProfileObservation{
		Duration: 5 * time.Second, PeakCores: 3, SustainedCores: 2, PeakMemoryBytes: 1 << 20, CPUMeasured: true,
	}); err != nil {
		t.Fatal(err)
	}

	body := getJSON[capacityExplainPayload](t, capacityExplainHandler(b),
		"/api/v1/capacity/profiles/explain?pipeline=demo", http.StatusOK)

	if len(body.Nodes) != 1 || body.Nodes[0].NodeID != "build" {
		t.Fatalf("nodes = %+v, want the build node row", body.Nodes)
	}
	if body.Profile.NodeCount != 1 {
		t.Errorf("node_count = %d, want 1", body.Profile.NodeCount)
	}
}

func TestCapacityExplain_RejectsMissingAndUnknownPipelines(t *testing.T) {
	st, b := capacityBackend(t)
	measure(t, st, "demo", 4)

	for _, tc := range []struct {
		name   string
		target string
		want   int
	}{
		{"no pipeline", "/api/v1/capacity/profiles/explain", http.StatusBadRequest},
		{"unmeasured pipeline", "/api/v1/capacity/profiles/explain?pipeline=nope", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			capacityExplainHandler(b)(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestCapacityExplain_AcceptsRepoScopedKeys(t *testing.T) {
	st, b := capacityBackend(t)
	measure(t, st, "myrepo/ci", 4)

	body := getJSON[capacityExplainPayload](t, capacityExplainHandler(b),
		"/api/v1/capacity/profiles/explain?pipeline=myrepo%2Fci", http.StatusOK)

	if body.Profile.Pipeline != "myrepo/ci" {
		t.Fatalf("pipeline = %q, want the repo-scoped key", body.Profile.Pipeline)
	}
}

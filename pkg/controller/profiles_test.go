package controller_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestFinishRun_FoldsProfilesAndEmitsPinDrift drives a cluster run to
// finish with measured node metrics well below its applied pin and asserts
// the controller folds the measurement into the pipeline profile and
// records a resource_pin_drift event -- the cluster-side counterpart of the
// local daemon's end-of-run drift warning.
func TestFinishRun_FoldsProfilesAndEmitsPinDrift(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	pipeline := "deploy"
	for range 2 {
		if err := st.RecordProfileObservation(ctx, pipeline, "node-1", store.ProfileObservation{
			Duration: time.Minute, PeakCores: 1, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertProfilePin(ctx, pipeline, "node-1", 4, 0); err != nil {
		t.Fatal(err)
	}

	start := time.Now().Add(-time.Minute)
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: pipeline, Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "node-1", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	for i := range 3 {
		if err := st.AddNodeMetricSample(ctx, "run-1", "node-1", store.MetricSample{
			TS: base.Add(time.Duration(i) * time.Second), CPUMillicores: 1000, MemoryBytes: 1 << 30,
		}); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	if err := c.FinishRun(ctx, "run-1", "success", ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	prof, err := st.GetPipelineProfile(ctx, pipeline, "node-1")
	if err != nil || prof == nil {
		t.Fatalf("profile after fold: %v", err)
	}
	if prof.SampleCount != 3 {
		t.Errorf("folded sample count = %d, want 3", prof.SampleCount)
	}

	events, err := st.ListEventsAfter(ctx, "run-1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == "resource_pin_drift" && e.NodeID == "node-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a resource_pin_drift event on node-1; got %+v", events)
	}
}

// TestFinishRun_NoPinNoDrift confirms an unpinned pipeline folds its
// measurement without ever emitting a drift event.
func TestFinishRun_NoPinNoDrift(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	for range 3 {
		_ = st.RecordProfileObservation(ctx, "build", "node-1", store.ProfileObservation{
			Duration: time.Minute, PeakCores: 2, PeakMemoryBytes: 2 << 30, CPUMeasured: true,
		})
	}
	if err := st.CreateRun(ctx, store.Run{ID: "run-2", Pipeline: "build", Status: "running", StartedAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-2", NodeID: "node-1", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	for i := range 2 {
		_ = st.AddNodeMetricSample(ctx, "run-2", "node-1", store.MetricSample{
			TS: base.Add(time.Duration(i) * time.Second), CPUMillicores: 2000, MemoryBytes: 2 << 30,
		})
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	if err := c.FinishRun(ctx, "run-2", "success", ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	events, _ := st.ListEventsAfter(ctx, "run-2", 0, 100)
	for _, e := range events {
		if e.Kind == "resource_pin_drift" {
			t.Errorf("unpinned pipeline must not emit drift; got %+v", e)
		}
	}
}

// TestGetPipelineProfile_RoundTripsThroughController exercises the read the
// cluster runner uses to size a pod, including the 404-as-nil path for an
// unprofiled pipeline.
func TestGetPipelineProfile_RoundTripsThroughController(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	prof, err := c.GetPipelineProfile(ctx, "unknown", "node-1")
	if err != nil {
		t.Fatalf("get missing profile: %v", err)
	}
	if prof != nil {
		t.Errorf("unprofiled pipeline should return nil profile, got %+v", prof)
	}

	if err := st.RecordProfileObservation(ctx, "deploy", "node-1", store.ProfileObservation{
		Duration: time.Minute, PeakCores: 3, PeakMemoryBytes: 4 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPipelinePin(ctx, "deploy", "node-1", 3, 4<<30); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	got, err := c.GetPipelineProfile(ctx, "deploy", "node-1")
	if err != nil || got == nil {
		t.Fatalf("get profile: %v", err)
	}
	if got.PeakCores != 3 || got.PinnedCores != 3 {
		t.Errorf("profile round trip lost data: %+v", got)
	}
}

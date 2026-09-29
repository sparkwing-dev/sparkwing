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
			Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true,
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

func TestFinishRun_NoPinNoDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
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
			Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true,
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

func TestFinishRun_RequiresUsableIntervalEvidence(t *testing.T) {
	good := store.MetricSample{Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true, CPUMillicores: 500, MemoryBytes: 4096}
	for _, tc := range []struct {
		name     string
		middle   store.MetricSample
		eligible bool
	}{
		{"valid", good, true},
		{"command", store.MetricSample{Kind: store.MetricCommand, CPUAvailable: true, MemoryAvailable: true, CPUTime: 120 * time.Second, CPUMillicores: 60000, MemoryBytes: 1 << 30}, true},
		{"unknown", store.MetricSample{CPUMillicores: 2000, MemoryBytes: 8192}, false},
		{"estimate", store.MetricSample{Kind: store.MetricEstimate, CPUAvailable: true, MemoryAvailable: true, CPUMillicores: 2000, MemoryBytes: 8192}, false},
		{"CPU gap", store.MetricSample{Kind: store.MetricInterval, MemoryAvailable: true, MemoryBytes: 4096}, false},
		{"memory gap", store.MetricSample{Kind: store.MetricInterval, CPUAvailable: true, CPUMillicores: 500}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			ctx := context.Background()
			start := time.Now().Add(-time.Minute)
			if err := st.CreateRun(ctx, store.Run{ID: "r", Pipeline: "p", Status: "running", StartedAt: start}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: "r", NodeID: "n", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			for i, sample := range []store.MetricSample{good, tc.middle, good} {
				sample.TS = start.Add(time.Duration(i) * 2 * time.Second)
				if err := st.AddNodeMetricSample(ctx, "r", "n", sample); err != nil {
					t.Fatal(err)
				}
			}
			srv := httptest.NewServer(controller.New(st, nil).Handler())
			defer srv.Close()
			if err := client.New(srv.URL, nil).FinishRun(ctx, "r", "success", ""); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"n", ""} {
				profile, err := st.GetPipelineProfile(ctx, "p", id)
				if err != nil {
					t.Fatal(err)
				}
				if !tc.eligible {
					if profile != nil {
						t.Errorf("%q incomplete evidence qualified: %+v", id, profile)
					}
					continue
				}
				if profile == nil || profile.SampleCount != 1 || !profile.CPUMeasured || profile.PeakCores != 0.5 || profile.PeakMemoryBytes != 4096 || profile.SustainedCores != nil {
					t.Errorf("%q peak policy changed: %+v", id, profile)
				}
			}
		})
	}
}

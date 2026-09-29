package k8s

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestResolveResources_PodLimitsUsePeakCPU(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sustained   float64
		wantHostCPU float64
	}{
		{"peak and sustained", 0.5, 0.5},
		{"peak only", 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			ctx := t.Context()
			for range 3 {
				if err := st.RecordProfileObservation(ctx, "deploy", "build", store.ProfileObservation{
					Duration: time.Minute, PeakCores: 2, PeakMemoryBytes: 512 << 20,
					SustainedCores: tc.sustained, CPUMeasured: true,
				}); err != nil {
					t.Fatal(err)
				}
			}
			srv := httptest.NewServer(controller.New(st, nil).Handler())
			t.Cleanup(srv.Close)
			node := sparkwing.Job(sparkwing.NewPlan(), "build", func(context.Context) error { return nil })
			r := &Runner{ctrl: client.New(srv.URL, nil), cfg: defaultsCfg}
			res := r.resolveResources(ctx, runner.Request{Pipeline: "deploy", NodeID: "build", Node: node})
			if res.Source != store.CostSourceMeasured || res.Cores != 2 || res.MemoryBytes != 512<<20 {
				t.Errorf("pod resolution = %+v; want measured 2 cores and 512 MiB", res)
			}
			resources := podResources(res, store.CPUClass{}, defaultsCfg)
			if milli(resources.Requests[corev1.ResourceCPU]) != 2000 || milli(resources.Limits[corev1.ResourceCPU]) != 4000 || bytesOf(resources.Requests[corev1.ResourceMemory]) != 512<<20 || bytesOf(resources.Limits[corev1.ResourceMemory]) != 640<<20 {
				t.Errorf("pod requests/limits = %+v; want CPU 2/4 cores and memory 512/640 MiB", resources)
			}
			profile, err := st.GetPipelineProfile(ctx, "deploy", "build")
			if err != nil {
				t.Fatal(err)
			}
			host := capacity.Resolve(nil, profile, 8, "")
			if host.Source != store.CostSourceMeasured || host.Cores != tc.wantHostCPU {
				t.Errorf("host resolution = %+v; want measured %v cores", host, tc.wantHostCPU)
			}
		})
	}
}

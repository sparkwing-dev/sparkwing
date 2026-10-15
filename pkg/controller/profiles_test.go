package controller_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestFinishRunFoldsPartialSamplesRaiseOnly(t *testing.T) {
	cases := []struct {
		name      string
		marker    store.MetricKind
		cores     int64
		memory    int64
		wantCount int
		wantCores float64
		wantMem   int64
	}{
		{"partial above raises", store.MetricPartial, 2000, 2000, 1, 2, 2000},
		{"partial below leaves the profile", store.MetricPartial, 500, 500, 1, 1, 1000},
		{"unknown stays excluded", store.MetricUnknown, 2000, 2000, 1, 1, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := st.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx := t.Context()
			start := time.Now().Add(-time.Minute)
			if err := teststore.ForTeam(t, st, store.DefaultTeam).CreateRun(ctx, store.Run{ID: "partial", Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: "partial", NodeID: "build", Status: "running", StartedAt: &start}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"build", ""} {
				if err := teststore.ForTeam(t, st, store.DefaultTeam).RecordProfileObservation(ctx, "demo", id, store.ProfileObservation{CPUMeasured: true, Duration: time.Minute, PeakCores: 1, SustainedCores: 1, PeakMemoryBytes: 1000}); err != nil {
					t.Fatal(err)
				}
			}
			for i, sample := range []store.MetricSample{
				{Kind: store.MetricInterval, TS: start, CPUMillicores: tc.cores, MemoryBytes: tc.memory},
				{Kind: store.MetricInterval, TS: start.Add(2 * time.Second), CPUMillicores: tc.cores, MemoryBytes: tc.memory},
				{Kind: tc.marker, TS: start.Add(4 * time.Second)},
			} {
				if err := st.AddNodeMetricSample(ctx, "partial", "build", sample); err != nil {
					t.Fatalf("sample %d: %v", i, err)
				}
			}
			if err := st.FinishNode(ctx, "partial", "build", "success", "", nil); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/partial/finish", strings.NewReader(`{"status":"success","error":""}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			controller.New(st, nil).Handler().ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("finish run: status %d, %s", response.Code, response.Body.String())
			}
			p, err := teststore.ForTeam(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "demo", "build")
			if err != nil {
				t.Fatal(err)
			}
			if p.SampleCount != tc.wantCount || p.PeakCores != tc.wantCores || p.PeakMemoryBytes != tc.wantMem {
				t.Fatalf("node profile = %+v, want count %d cores %v memory %d", p, tc.wantCount, tc.wantCores, tc.wantMem)
			}
		})
	}
}

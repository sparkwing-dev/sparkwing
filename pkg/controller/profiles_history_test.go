package controller_test

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestFinishRunCannotRelearnPreUpgradeMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	seed := func(s *store.Store, id string) {
		t.Helper()
		if err := s.CreateRun(t.Context(), store.Run{ID: id, Pipeline: "history", Status: "running", StartedAt: time.Unix(100, 0)}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateNode(t.Context(), store.Node{RunID: id, NodeID: id, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddNodeMetricSample(t.Context(), id, id, store.MetricSample{Kind: store.MetricInterval, TS: time.Unix(101, 0), CPUMillicores: 500, MemoryBytes: 200}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishNode(t.Context(), id, id, "success", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	seed(st, "old")
	for _, node := range []string{"", "old"} {
		if err := st.RecordProfileObservation(t.Context(), "history", node, store.ProfileObservation{Duration: time.Second, PeakCores: 14, PeakMemoryBytes: 450, CPUMeasured: true}); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{`UPDATE pipeline_profiles SET samples_json = '{"schema":10,"samples":[{"d":1000000000,"c":14,"s":14,"m":450}]}'`, `DELETE FROM sparkwing_schema_version WHERE version >= 51`, `DELETE FROM sparkwing_requirements WHERE name = 'process-tree-accounting'`} {
		if _, err := st.DB().ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(controller.New(st, nil).Handler())
	defer server.Close()
	c := client.New(server.URL, nil)
	for range 2 {
		if err := c.FinishRun(t.Context(), "old", "success", ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, node := range []string{"", "old"} {
		profile, err := st.GetPipelineProfile(t.Context(), "history", node)
		if err != nil || (profile != nil && (profile.SampleCount != 0 || profile.CPUMeasured)) {
			t.Fatalf("historical finish relearned incompatible metrics: %+v, %v", profile, err)
		}
	}
	seed(st, "fresh")
	if err := c.FinishRun(t.Context(), "fresh", "success", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishRun(t.Context(), "old", "success", ""); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"", "fresh"} {
		profile, err := st.GetPipelineProfile(t.Context(), "history", node)
		if err != nil || profile == nil || profile.SampleCount != 1 || !profile.CPUMeasured || profile.PeakCores != 0.5 || profile.PeakMemoryBytes != 200 {
			t.Fatalf("fresh source lost or old source counted: %+v, %v", profile, err)
		}
	}
}

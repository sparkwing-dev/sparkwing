package controller_test

import (
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestFinishRunRetriesDoNotRepeatProfileObservations(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(map[bool]string{false: "sequential", true: "concurrent"}[concurrent], func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			server := httptest.NewServer(controller.New(st, nil).Handler())
			t.Cleanup(server.Close)
			c := client.New(server.URL, nil)
			seed := func(id string) {
				t.Helper()
				if err := st.CreateRun(t.Context(), store.Run{ID: id, Pipeline: "finish-retry", Status: "running", StartedAt: time.Unix(100, 0)}); err != nil {
					t.Fatal(err)
				}
				if err := st.CreateNode(t.Context(), store.Node{RunID: id, NodeID: "build", Status: "pending"}); err != nil {
					t.Fatal(err)
				}
				if err := st.AddNodeMetricSample(t.Context(), id, "build", store.MetricSample{Kind: store.MetricInterval, TS: time.Unix(101, 0), CPUMillicores: 500, MemoryBytes: 200}); err != nil {
					t.Fatal(err)
				}
				if err := st.FinishNode(t.Context(), id, "build", "success", "", nil); err != nil {
					t.Fatal(err)
				}
			}
			seed("first")
			for _, status := range []string{"", "running", "pending", "invalid"} {
				if err := c.FinishRun(t.Context(), "first", status, ""); err == nil {
					t.Fatalf("HTTP completion accepted nonterminal status %q", status)
				}
			}
			initial, err := st.GetRun(t.Context(), "first")
			if err != nil || initial == nil || initial.Status != "running" || initial.FinishedAt != nil {
				t.Fatalf("invalid completion changed run=%+v error=%v", initial, err)
			}
			var wg sync.WaitGroup
			finish := func() {
				if err := c.FinishRun(t.Context(), "first", "success", ""); err != nil {
					t.Error(err)
				}
			}
			for range 8 {
				if concurrent {
					wg.Add(1)
					go func() { defer wg.Done(); finish() }()
				} else {
					finish()
				}
			}
			wg.Wait()
			before, err := st.GetRun(t.Context(), "first")
			if err != nil || before == nil || before.FinishedAt == nil {
				t.Fatalf("finished run=%+v, %v", before, err)
			}
			if err := c.FinishRun(t.Context(), "first", "failed", "conflicting retry"); err == nil {
				t.Fatal("conflicting retry accepted, want a 400 naming the committed outcome")
			}
			after, err := st.GetRun(t.Context(), "first")
			if err != nil || after == nil || after.FinishedAt == nil || after.Status != "success" || after.Error != "" || !after.FinishedAt.Equal(*before.FinishedAt) {
				t.Errorf("retry changed terminal state: before=%+v after=%+v error=%v", before, after, err)
			}
			for _, id := range []string{"", "build"} {
				profile, err := st.GetPipelineProfile(t.Context(), "finish-retry", id)
				if err != nil || profile == nil || profile.SampleCount != 1 {
					t.Errorf("retried profile %q=%+v, %v; want one observation", id, profile, err)
				}
			}
			seed("second")
			if err := c.FinishRun(t.Context(), "second", "success", ""); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"", "build"} {
				profile, err := st.GetPipelineProfile(t.Context(), "finish-retry", id)
				if err != nil || profile == nil || profile.SampleCount != 2 {
					t.Errorf("distinct run profile %q=%+v, %v; want two observations", id, profile, err)
				}
			}
		})
	}
}

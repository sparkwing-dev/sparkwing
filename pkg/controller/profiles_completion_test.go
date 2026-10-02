package controller_test

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestFinishRunExcludesIncompleteAndRetriedNodes(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"failed", `UPDATE nodes SET outcome = 'failed' WHERE node_id = 'build'`},
		{"cancelled", `UPDATE nodes SET outcome = 'cancelled' WHERE node_id = 'build'`},
		{"unfinished", `UPDATE nodes SET status = 'running', outcome = '' WHERE node_id = 'build'`},
		{"local retry", ""},
		{"successful retry", `UPDATE nodes SET attempts_consumed = 2 WHERE node_id = 'build'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			ctx := t.Context()
			if err := st.CreateRun(ctx, store.Run{ID: "run", Pipeline: "completion", Status: "running", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			for _, node := range []string{"build", "good"} {
				if err := st.CreateNode(ctx, store.Node{RunID: "run", NodeID: node, Status: "pending"}); err != nil {
					t.Fatal(err)
				}
				if err := st.AddNodeMetricSample(ctx, "run", node, store.MetricSample{Kind: store.MetricInterval, TS: time.Unix(100, 0), CPUMillicores: 1000, MemoryBytes: 100}); err != nil {
					t.Fatal(err)
				}
				if tc.name == "local retry" && node == "build" {
					for ordinal := 1; ordinal <= 2; ordinal++ {
						if err := st.AcknowledgeNodeExecutionStart(ctx, "run", node, store.ClaimIdentity{}, store.ExecutionStart{ExecutorKind: store.ExecutorKindLocal, ExecutorID: "host", AttemptOrdinal: ordinal}); err != nil {
							t.Fatal(err)
						}
						if err := st.FinishNodeExecutionAttempt(ctx, "run", node, store.ClaimIdentity{}, store.ExecutionAttemptFinish{ExecutorKind: store.ExecutorKindLocal, AttemptOrdinal: ordinal, Outcome: "success"}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := st.FinishNode(ctx, "run", node, "success", "", nil); err != nil {
					t.Fatal(err)
				}
			}
			if tc.query != "" {
				if _, err := st.DB().ExecContext(ctx, tc.query); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(controller.New(st, nil).Handler())
			defer server.Close()
			if err := client.New(server.URL, nil).FinishRun(ctx, "run", "success", ""); err != nil {
				t.Fatal(err)
			}
			for _, node := range []string{"", "build", "good"} {
				profile, err := st.GetPipelineProfile(ctx, "completion", node)
				if err != nil {
					t.Fatal(err)
				}
				if node == "good" {
					if profile == nil || profile.SampleCount != 1 {
						t.Fatalf("independent successful node lost: %+v", profile)
					}
				} else if profile != nil && profile.SampleCount != 0 {
					t.Fatalf("incomplete execution learned for %q: %+v", node, profile)
				}
			}
		})
	}
}

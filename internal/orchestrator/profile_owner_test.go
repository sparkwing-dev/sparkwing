package orchestrator

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunProfileHasOneRecorder(t *testing.T) {
	for _, daemon := range []bool{true, false} {
		t.Run(map[bool]string{true: "daemon", false: "workspace controller"}[daemon], func(t *testing.T) {
			var backends Backends
			var st *store.Store
			if daemon {
				home := wingdTestHome(t)
				sock, held := startAPIDaemon(t, home, nil)
				var closeConnections func()
				backends, closeConnections = HostedBackends(PathsAt(home), sock, nil)
				t.Cleanup(closeConnections)
				var err error
				st, _, err = held.Create(t.Context())
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var err error
				st, err = teststore.Open(filepath.Join(t.TempDir(), "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = st.Close() })
				server := httptest.NewServer(controller.New(st, nil).WithLocalExecution().Handler())
				t.Cleanup(server.Close)
				backends = RemoteBackends(client.New(server.URL, nil), nil, nil, nil, time.Minute)
			}
			if backends.LocalCoordination != daemon {
				t.Fatalf("local recorder ownership=%t, daemon=%t", backends.LocalCoordination, daemon)
			}
			start := time.Unix(100, 0)
			if err := backends.State.CreateRun(t.Context(), store.Run{ID: "run", Pipeline: "ownership", Status: "running", StartedAt: start}); err != nil {
				t.Fatal(err)
			}
			if err := backends.State.CreateNode(t.Context(), store.Node{RunID: "run", NodeID: "build", Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			if err := backends.State.AddNodeMetricSample(t.Context(), "run", "build", store.MetricSample{Kind: store.MetricInterval, TS: start.Add(time.Second), CPUMillicores: 500, MemoryBytes: 200}); err != nil {
				t.Fatal(err)
			}
			if err := backends.State.FinishNode(t.Context(), "run", "build", "success", "", nil); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := backends.State.FinishRun(t.Context(), "run", "success", ""); err != nil {
					t.Fatal(err)
				}
			}
			assertProfileObservationCount := func(want int) {
				t.Helper()
				for _, node := range []string{"", "build"} {
					profile, err := st.GetPipelineProfile(t.Context(), "ownership", node)
					if err != nil {
						t.Fatal(err)
					}
					count := 0
					if profile != nil {
						count = profile.SampleCount
					}
					if count != want {
						t.Errorf("profile %q observations=%d, want %d", node, count, want)
					}
				}
			}
			if backends.LocalCoordination {
				assertProfileObservationCount(0)
				recordRunProfile(t.Context(), backends.State, "ownership", "run", nil, "", runCharge{}, false, start, start.Add(2*time.Second))
			}
			assertProfileObservationCount(1)
		})
	}
}

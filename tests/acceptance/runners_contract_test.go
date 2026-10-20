package acceptance_test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSurfaceRunnerTransportPreservesGrantedAndObservedFacts(t *testing.T) {
	want := controllerwire.RunnerDetail{Name: "desktop", Class: "self_hosted", Location: "local", Labels: []string{"os=linux", "gpu"}, ReservedPools: []string{"prod"}, Repos: []string{"acme/api"}, Status: "busy", LastSeen: "2026-10-14T01:02:03Z", MaxConcurrent: 2, Metered: true, Capacity: controllerwire.RunnerResources{Cores: 6.5, MemoryBytes: 7 << 30}, ActiveJobs: []controllerwire.RunnerJob{{RunID: "r1", JobID: "build"}}, Headroom: &controllerwire.RunnerHeadroom{RunnerResources: controllerwire.RunnerResources{Cores: 1.5, MemoryBytes: 2 << 30}, QueueDepth: 3, ObservedAt: "2026-10-14T01:02:03Z"}}
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		reader, err := client.NewRunnerReader(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
			if req.Method != "GET" || req.URL.Path != "/api/v1/runners" {
				t.Fatalf("runner read: %s %s", req.Method, req.URL)
			}
			data, err := json.Marshal(controllerwire.Runners{Runners: []controllerwire.RunnerDetail{want}})
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := reader.ListRunners(t.Context())
		if err != nil || !reflect.DeepEqual(got.Runners, []controllerwire.RunnerDetail{want}) {
			t.Fatalf("runner facts: %+v %v", got, err)
		}
	}
}

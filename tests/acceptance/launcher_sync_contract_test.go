package acceptance_test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSurfaceLauncherSyncAwardAndJobInstance(t *testing.T) {
	previous := controllerwire.LauncherJobRef{Namespace: "ci", Name: "job-1", UID: "previous-instance"}
	current := controllerwire.LauncherJobRef{Namespace: "ci", Name: "job-1", UID: "current-instance"}
	input := controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{{
		Award: capability.LeaseRef{Kind: capability.UnitNode, ID: "stable-unit", Holder: "launcher", Generation: 7}, Job: current, NodeUID: "node-instance",
	}}}
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			controller, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != "POST" || req.URL.Path != "/api/v1/launcher/sync" {
					t.Fatalf("sync route changed: %s %s", req.Method, req.URL)
				}
				var got controllerwire.LauncherSyncRequest
				if err := json.NewDecoder(req.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, input) {
					t.Fatalf("award or Job instance changed: %+v %v", got, err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"awards":[],"remove_reservations":[],"remove":[{"namespace":"ci","name":"job-1","uid":"previous-instance"}]}`))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := controller.SyncLauncher(t.Context(), input)
			if err != nil || !reflect.DeepEqual(result.Remove, []controllerwire.LauncherJobRef{previous}) || result.Remove[0] == current {
				t.Fatalf("cleanup lost its Job-instance precondition: %+v %v", result, err)
			}
		})
	}
}

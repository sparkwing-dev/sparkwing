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

func TestSurfaceLauncherSyncAwardRetirement(t *testing.T) {
	fence := capability.LeaseRef{Kind: capability.UnitNode, ID: "stable-unit", Holder: "launcher", Generation: 7}
	input := controllerwire.LauncherSyncRequest{Observed: []capability.LauncherObservation{{Award: fence}}}
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			controller, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || req.URL.Path != "/api/v1/launcher/sync" {
					t.Fatalf("sync route changed: %s %s", req.Method, req.URL)
				}
				var got controllerwire.LauncherSyncRequest
				if err := json.NewDecoder(req.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, input) {
					t.Fatalf("award fence changed: %+v %v", got, err)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"awards":[],"retired":[{"kind":"node","id":"stable-unit","holder":"launcher","generation":7}]}`))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := controller.SyncLauncher(t.Context(), input)
			if err != nil || !reflect.DeepEqual(result.Retired, []capability.LeaseRef{fence}) {
				t.Fatalf("retirement lost its award fence: %+v %v", result, err)
			}
		})
	}
}

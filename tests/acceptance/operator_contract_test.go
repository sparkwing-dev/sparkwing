package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSurfaceOperatorManagementTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		limits := controllerwire.OperatorLimits{Limits: map[string]int64{"max_jobs_per_run": 0}}
		state := controllerwire.ObjectStoreBreaker{Classes: []controllerwire.ObjectStoreClassState{}, Ceiling: controllerwire.ObjectStoreCeiling{Incomplete: true, MeasureError: "measurement unavailable"}}
		requests := []struct {
			method, path string
			value        any
		}{
			{"GET", "/api/v1/operator/limits", limits},
			{"PUT", "/api/v1/operator/limits", limits},
			{"GET", "/api/v1/operator/object-store/breaker", state},
			{"POST", "/api/v1/operator/object-store/reset-breaker", state},
		}
		calls := 0
		manager, err := client.NewOperatorManager(client.EngineOptions{URL: endpoint, Bearer: func(context.Context) (string, error) { return "deployment-credential", nil }, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
			if calls >= len(requests) {
				t.Fatal("unexpected operation")
			}
			want := requests[calls]
			calls++
			if req.Method != want.method || req.URL.Path != want.path || req.Header.Get("Authorization") != "Bearer deployment-credential" {
				t.Fatalf("operation changed: %s %s", req.Method, req.URL)
			}
			if req.Method == "POST" && req.Header.Get("Idempotency-Key") != "one-reset" {
				t.Fatal("reset lost its replay identity")
			}
			if req.Method == "PUT" {
				var got controllerwire.OperatorLimitsWrite
				if json.NewDecoder(req.Body).Decode(&got) != nil || !reflect.DeepEqual(got.Limits, limits.Limits) {
					t.Fatal("explicit zero changed")
				}
			}
			body, err := json.Marshal(want.value)
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		read, err := manager.ReadOperatorLimits(t.Context())
		if err != nil || !reflect.DeepEqual(read, limits) {
			t.Fatalf("read changed: %+v %v", read, err)
		}
		written, err := manager.WriteOperatorLimits(t.Context(), controllerwire.OperatorLimitsWrite{Limits: limits.Limits})
		if err != nil || !reflect.DeepEqual(written, limits) {
			t.Fatalf("write changed: %+v %v", written, err)
		}
		breaker, err := manager.ReadObjectStoreBreaker(t.Context())
		if err != nil || !reflect.DeepEqual(breaker, state) {
			t.Fatalf("measurement changed: %+v %v", breaker, err)
		}
		reset, err := manager.ResetObjectStoreBreaker(t.Context(), "one-reset")
		if err != nil || !reflect.DeepEqual(reset, state) || calls != len(requests) {
			t.Fatalf("reset changed: calls=%d error=%v", calls, err)
		}
	}
}

package acceptance_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
)

func TestRecoveryFailureAbsenceContract(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			c, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.RequestURI() != "/api/v1/runs/run/jobs/source/failure?generation=2" {
					t.Fatalf("failure read: %s %s", req.Method, req.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("null"))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			failure, err := c.ReadFailure(t.Context(), "run", "source", 2)
			if err != nil || failure != nil {
				t.Fatalf("synthesized recovery failure: %+v %v", failure, err)
			}
		})
	}
}

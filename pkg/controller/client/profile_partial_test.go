package client

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRecordProfileObservationSendsPartialFlag(t *testing.T) {
	var received bool
	transport := transientRoundTripper(func(req *http.Request) (*http.Response, error) {
		var body struct {
			Partial bool `json:"partial"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		received = body.Partial
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	})
	c := New("http://example.invalid", &http.Client{Transport: transport})
	if err := c.RecordProfileObservation(t.Context(), "demo", "build", store.ProfileObservation{CPUMeasured: true, Partial: true, PeakCores: 2}); err != nil {
		t.Fatal(err)
	}
	if !received {
		t.Fatal("partial observation arrived without its lower-bound flag")
	}
}

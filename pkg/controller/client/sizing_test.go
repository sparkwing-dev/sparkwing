package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSizingQueryUsesAuthorizedReadAndPreservesMeasurements(t *testing.T) {
	want := controllerwire.SizingDetail{Pipeline: "check", Repo: "acme/api", Source: "floor", MemoExcluded: 3, Rollup: controllerwire.SizingMeasurements{FloorCores: 6.5, FloorMemoryBytes: 7 << 30, CPUP50: 0.5, CPUP95: 1.25, CPUMeasured: true, PrevPeakCores: 2}, Jobs: []controllerwire.JobSizing{}}
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if req.Method != "GET" || req.URL.Path != "/api/v1/sizing" || q.Get("pipeline") != "check" || q.Get("limit") != "1" || q.Get("cursor") != "opaque" {
			t.Fatalf("sizing: %s %s", req.Method, req.URL)
		}
		body, err := json.Marshal(controllerwire.SizingPage{Sizing: []controllerwire.SizingDetail{want}})
		if err != nil {
			t.Fatal(err)
		}
		return engineResponse(200, string(body)), nil
	})
	got, err := c.ListSizing(t.Context(), controllerwire.SizingQuery{Pipeline: "check", Limit: 1, Cursor: "opaque"})
	if err != nil || len(got.Sizing) != 1 || got.Sizing[0].Rollup.FloorCores != 6.5 || got.Sizing[0].Rollup.FloorMemoryBytes != 7<<30 || got.Sizing[0].MemoExcluded != 3 {
		t.Fatalf("sizing: %+v %v", got, err)
	}
}

func TestSizingResetRefusesBeforeHTTPWithoutPolicy(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("reset used an unregistered route")
		return nil, nil
	})
	_, err := c.ResetSizing(t.Context(), controllerwire.SizingReset{Pipeline: "check"})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.PolicyMissing {
		t.Fatalf("missing route: %v", err)
	}
}

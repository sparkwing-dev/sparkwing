package client

import (
	"net/http"
	"testing"
)

func TestRunPrunerUsesAuthorizedDelete(t *testing.T) {
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "DELETE" || req.URL.Path != "/api/v1/runs/412" {
			t.Fatalf("request: %s %s", req.Method, req.URL)
		}
		return engineResponse(200, `{"run_id":"r1","deleted":true}`), nil
	})
	result, err := c.DeleteRun(t.Context(), "412")
	if err != nil || result.RunID != "r1" || !result.Deleted {
		t.Fatalf("result: %+v %v", result, err)
	}
}

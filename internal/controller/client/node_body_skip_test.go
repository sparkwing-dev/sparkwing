package client

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestNodeMemoEvidenceUsesPrivateHeader(t *testing.T) {
	c := testEngine(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get(controllerwire.NodeBodySkippedHeader) != "memo" {
			t.Fatal("missing executor memo evidence")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		extra := body["extra"].(map[string]any)
		if len(extra) != 0 {
			t.Fatalf("executor evidence leaked into application payload: %+v", extra)
		}
		encoded, err := json.Marshal(runstate.Applied{})
		if err != nil {
			t.Fatal(err)
		}
		return engineResponse(http.StatusOK, string(encoded)), nil
	})
	body := runstate.NodeCommandPayload{To: runstate.NodeMemoized, Cause: runstate.CauseReport, Extra: runstate.NodeCommandExtra{BodySkipped: "memo"}}
	var applied runstate.Applied
	if err := c.request(t.Context(), "POST", "/api/v1/runs/{run}/local-host/nodes/{job}/transition", "/api/v1/runs/run/local-host/nodes/job/transition", "", body, &applied); err != nil {
		t.Fatal(err)
	}
}

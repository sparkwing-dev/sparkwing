package client

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestNodeExitObservationPreservesLegacyCommandBody(t *testing.T) {
	for _, cause := range []runstate.TransitionCause{runstate.CauseReport, runstate.CauseRetry} {
		t.Run(string(cause), func(t *testing.T) {
			exited := time.Unix(1000, 123).UTC()
			c := testEngine(t, func(r *http.Request) (*http.Response, error) {
				if got := r.Header.Get(controllerwire.NodeExitedAtHeader); got != exited.Format(time.RFC3339Nano) {
					t.Fatalf("exit observation header = %q", got)
				}
				var legacy struct {
					To            runstate.NodeState       `json:"to"`
					Cause         runstate.TransitionCause `json:"cause"`
					SourceAttempt *runspec.AttemptOrdinal  `json:"source_attempt"`
					Extra         struct {
						Result *runstate.NodeResult `json:"result,omitempty"`
					} `json:"extra"`
				}
				decoder := json.NewDecoder(r.Body)
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&legacy); err != nil {
					t.Fatalf("older controller refused observed exit body: %v", err)
				}
				if legacy.Cause != cause || legacy.Extra.Result == nil {
					t.Fatal("observation changed the result command")
				}
				encoded, err := json.Marshal(runstate.Applied{})
				if err != nil {
					t.Fatal(err)
				}
				return engineResponse(http.StatusOK, string(encoded)), nil
			})
			body := runstate.NodeCommandPayload{To: runstate.NodeSuccess, Cause: cause, SourceAttempt: new(runspec.AttemptOrdinal(1)), Extra: runstate.NodeCommandExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}, ExitedAt: &exited}}
			var applied runstate.Applied
			if err := c.request(t.Context(), "POST", "/api/v1/runs/{run}/local-host/nodes/{job}/transition", "/api/v1/runs/run/local-host/nodes/job/transition", "", body, &applied); err != nil {
				t.Fatal(err)
			}
		})
	}
}

package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestNodeMemoHeaderParsing(t *testing.T) {
	for _, test := range []struct {
		name    string
		values  []string
		cause   runstate.TransitionCause
		outcome runstate.NodeState
		valid   bool
	}{
		{"absent", nil, runstate.CauseStart, runstate.NodeRunning, true},
		{"memo", []string{"memo"}, runstate.CauseReport, runstate.NodeMemoized, true},
		{"skip_if", []string{"skip_if"}, runstate.CauseReport, runstate.NodeSkipped, false},
		{"empty", []string{""}, runstate.CauseReport, runstate.NodeMemoized, false},
		{"unknown", []string{"cache"}, runstate.CauseReport, runstate.NodeMemoized, false},
		{"multiple", []string{"memo", "memo"}, runstate.CauseReport, runstate.NodeMemoized, false},
		{"joined", []string{"memo, memo"}, runstate.CauseReport, runstate.NodeMemoized, false},
		{"start", []string{"memo"}, runstate.CauseStart, runstate.NodeMemoized, false},
		{"retry", []string{"memo"}, runstate.CauseRetry, runstate.NodeMemoized, false},
		{"wrong_outcome", []string{"memo"}, runstate.CauseReport, runstate.NodeSkipped, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			for _, v := range test.values {
				request.Header.Add(controllerwire.NodeBodySkippedHeader, v)
			}
			body := runstate.NodeCommandPayload{Cause: test.cause, To: test.outcome}
			err := observeNodeBodySkip(request, &body)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t error=%v", test.valid, err)
			}
			if err == nil && len(test.values) > 0 && body.Extra.BodySkipped != "memo" {
				t.Fatalf("missing memo claim: %+v", body.Extra)
			}
		})
	}
}

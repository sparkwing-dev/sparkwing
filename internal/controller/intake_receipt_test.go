package controller

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func storedIntake(t testing.TB, st *store.Store, id string) runspec.SealedRequest {
	t.Helper()
	team, err := st.AsOperator().RunTeam(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	row, err := teststore.ForTeam(t, st, team).ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Request
}

func TestMemberIntakeAndRetryResponsesAreDisplayOnly(t *testing.T) {
	sealed, err := runspec.SealRequest(runspec.Request{Version: 1, Team: "default", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: strings.Repeat("a", 40)}, Pipeline: "deploy", RequestedArgs: map[string]string{"token": "original-private-token", "public": "visible"}, SecretArgs: runspec.Optional[[]string]{Set: true, Value: []string{"token"}}, Source: runspec.Source{Ref: strings.Repeat("a", 40), Location: runspec.SourceLocation{LocalCheckout: "/private/checkout"}}}, runspec.IntakeFacts{RunID: "original"})
	if err != nil {
		t.Fatal(err)
	}
	for _, replayed := range []bool{false, true} {
		response := createRunResponse(store.AdmissionResult{RunID: "retry", Number: 2, Request: sealed, Replayed: replayed})
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"original-private-token", "/private/checkout", string(sealed.Hash)} {
			if strings.Contains(string(raw), private) {
				t.Fatal("member receipt exposed execution inputs")
			}
		}
		if !strings.Contains(string(raw), `"token":"***"`) || !strings.Contains(string(raw), `"public":"visible"`) {
			t.Fatal("receipt lost display classification")
		}
	}
}

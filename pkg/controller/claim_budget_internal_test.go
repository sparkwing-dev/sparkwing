package controller

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestClaimBudgetAnAwardDoesNotSpendBudget(t *testing.T) {
	engine, _, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	s := New(engine.Store, nil).WithEngineController(engine).WithRequestBudget(RequestBudget{ClaimsPerMinute: 1})
	call := func(want int) *httptest.ResponseRecorder {
		t.Helper()
		return engineClaimHTTP(t, s.Handler(), raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"],"available_cores":1}`, want)
	}
	if w := call(200); !strings.Contains(w.Body.String(), `"capability"`) {
		t.Fatalf("no award: %s", w.Body)
	}
	if w := call(200); strings.Contains(w.Body.String(), `"capability"`) {
		t.Fatalf("repeated award: %s", w.Body)
	}
	call(429)
}

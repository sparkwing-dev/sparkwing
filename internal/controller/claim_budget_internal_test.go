package controller

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
	refused := call(429)
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(refused.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	failure, err := apierr.DecodeInner(envelope.Error)
	if err != nil || failure.Code != apierr.RateLimited || refused.Header().Get("Retry-After") == "" {
		t.Fatalf("refusal is not the standard envelope: %s %v", refused.Body, err)
	}
	if delay, ok := apierr.RetryDelay(failure); !ok || delay <= 0 || strconv.FormatInt(int64(delay/time.Second), 10) != refused.Header().Get("Retry-After") {
		t.Fatalf("retry delay %s %t disagrees with Retry-After %q", delay, ok, refused.Header().Get("Retry-After"))
	}
}

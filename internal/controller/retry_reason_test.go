package controller_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAgentRetryRequiresAReasonThatTheNewRunRecords(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	first := floodRun(t, f.intake(f.token, computeIntake("build")), http.StatusCreated)
	if err := teststore.SeedRunFinish(f.st, t.Context(), store.DefaultTeam, first.RunID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	retry := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+first.RunID+"/retry", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+f.token)
		return f.serve(req)
	}
	if w := retry(`{"mode":"all","idempotency_key":"agent-no-reason","agent":"claude-code"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "--reason") || !strings.Contains(w.Body.String(), "flake: DNS timeout in TestX") {
		t.Fatalf("agent retry without a reason = %d %s; want a 400 naming --reason", w.Code, w.Body)
	}
	for _, reason := range []string{"   ", "line\nbreak", "bad\x1b[31m", strings.Repeat("a", 1025)} {
		body, _ := json.Marshal(controllerwire.RetryRequest{Mode: controllerwire.RetryAll, IdempotencyKey: "bad-reason", Reason: reason})
		if w := retry(string(body)); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "reason") {
			t.Fatalf("reason %q = %d %s; want a 400", reason, w.Code, w.Body)
		}
	}
	retried := floodRun(t, retry(`{"mode":"all","idempotency_key":"agent-reason","agent":"claude-code","reason":"flake: DNS timeout in TestX"}`), http.StatusOK)

	list := f.cookieRequest(http.MethodGet, "/api/v1/runs", "")
	list.Header.Set("Accept", "application/json")
	w := f.serve(list)
	var page controllerwire.RunPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil {
		t.Fatalf("GET runs = %d %s", w.Code, w.Body)
	}
	found := false
	for _, run := range page.Runs {
		lineage := run.Inputs.Lineage
		if run.ID == retried.RunID {
			found = lineage.RetryOf == first.RunID && lineage.RetryReason == "flake: DNS timeout in TestX"
		} else if lineage.RetryReason != "" {
			t.Fatalf("source run %s has retry reason %q", run.ID, lineage.RetryReason)
		}
	}
	if !found {
		t.Fatalf("retry %s does not record its reason: %s", retried.RunID, w.Body)
	}

	human := floodRun(t, retry(`{"mode":"failed","idempotency_key":"human"}`), http.StatusOK)
	if human.RunID == retried.RunID {
		t.Fatal("human retry replayed the agent retry")
	}
}

func TestRetryReasonRequiresASourceRun(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	body := computeIntake("build")
	body.Request.Lineage.RetryReason = "flake"
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	if w := f.serve(req); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "source run") {
		t.Fatalf("intake with a retry reason and no source = %d %s; want a 400", w.Code, w.Body)
	}
}

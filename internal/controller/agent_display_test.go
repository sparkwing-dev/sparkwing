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
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAgentLabelIsSealedAtIntakeAndKeptByRetry(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	body := computeIntake("build")
	body.Request.Agent = "claude-code:land-agent"
	first := floodRun(t, f.intake(f.token, body), http.StatusCreated)
	if err := teststore.SeedRunFinish(f.st, t.Context(), store.DefaultTeam, first.RunID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	if first.Inputs.Agent != "claude-code:land-agent" {
		t.Fatalf("intake response inputs agent = %q", first.Inputs.Agent)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+first.RunID+"/retry", strings.NewReader(`{"mode":"all","idempotency_key":"agent-retry"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	retried := floodRun(t, f.serve(req), http.StatusOK)

	for _, run := range []string{first.RunID, retried.RunID} {
		detail := f.cookieRequest(http.MethodGet, "/api/v1/runs/"+run, "")
		detail.Header.Set("Accept", "application/json")
		w := f.serve(detail)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"agent":"claude-code:land-agent"`) {
			t.Fatalf("GET run %s = %d %s; want the sealed agent", run, w.Code, w.Body)
		}
	}
	console := f.serve(f.cookieRequest(http.MethodGet, "/api/v1/runs", ""))
	var summaries struct {
		Runs []runstate.RunSummary `json:"runs"`
	}
	if console.Code != http.StatusOK || json.Unmarshal(console.Body.Bytes(), &summaries) != nil || len(summaries.Runs) != 2 {
		t.Fatalf("GET runs = %d %s", console.Code, console.Body)
	}
	for _, run := range summaries.Runs {
		if run.Agent != "claude-code:land-agent" || run.TriggerUser == "" {
			t.Fatalf("listed run %s agent %q requester %q; want both", run.ID, run.Agent, run.TriggerUser)
		}
	}
	list := f.cookieRequest(http.MethodGet, "/api/v1/runs", "")
	list.Header.Set("Accept", "application/json")
	w := f.serve(list)
	var page controllerwire.RunPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Runs) != 2 {
		t.Fatalf("GET runs as JSON = %d %s", w.Code, w.Body)
	}
	for _, run := range page.Runs {
		if run.Inputs.Agent != "claude-code:land-agent" {
			t.Fatalf("paged run %s agent %q", run.ID, run.Inputs.Agent)
		}
	}
}

func TestAgentLabelRefusesUnsafeValues(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	for _, agent := range []string{"none", "claude code", "bad\x1b[31m", ":leading", strings.Repeat("a", 65)} {
		body := computeIntake("build")
		body.Request.Agent = agent
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+f.token)
		if w := f.serve(req); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "agent") {
			t.Fatalf("agent %q = %d %s; want a 400 naming the agent label", agent, w.Code, w.Body)
		}
	}
}

package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestRunControlHTTPUsesStoreTransactions(t *testing.T) {
	s, session, csrf := reviewAccount(t, "editor")
	response := reviewSessionRequest(t, s, session, csrf, "POST", "/api/v1/tokens", `{"grants":["runs.read","runs.control","approvals.write"]}`, 201)
	var token credentialResponse
	if err := json.Unmarshal(response.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	for _, job := range []store.Node{{RunID: "run", NodeID: "work", Status: "pending"}, {RunID: "run", NodeID: "gate", Status: "approval_pending"}} {
		if err := teststore.SeedNode(s.store, t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}
	engineSQL(t, s.store, `INSERT INTO approvals(team,run_id,node_id,requested_at) VALUES ('a','run','gate',?)`, time.Now().UnixNano())
	request := func(path, body, key string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token.Capability)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s=%d %s, want %d", path, w.Code, w.Body, status)
		}
		return w
	}
	for range 2 {
		request("/api/v1/runs/run/debug-pauses", `{"job_id":"work","reason":"manual"}`, "pause", 201)
	}
	request("/api/v1/runs/run/debug-pauses", `{"job_id":"work","reason":"changed"}`, "pause", 409)
	for i := range 2 {
		w := request("/api/v1/runs/run/approvals/gate", `{"resolution":"approved","comment":"go"}`, "approval", 200)
		var result controllerwire.ApprovalResult
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Replayed != (i == 1) || result.Approval.Approver != "member" {
			t.Fatalf("approval=%+v", result)
		}
	}
	engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
	request("/api/v1/runs/run/debug-pauses", `{"job_id":"work","reason":"manual"}`, "pause", 401)
}

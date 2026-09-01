package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newBounceTestServer(t *testing.T) (*Server, string, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	raw, _, err := st.CreateToken("alice", store.TokenKindUser,
		[]string{ScopeRunsWrite, ScopeNodesClaim}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, id := range []string{"build", "deploy"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: id, Status: "pending"}); err != nil {
			t.Fatalf("CreateNode: %v", err)
		}
	}
	if err := st.StartNode(ctx, "run-1", "build"); err != nil {
		t.Fatalf("StartNode: %v", err)
	}
	return New(st, nil).WithAuthenticator(NewAuthenticator(st, 0)), raw, st
}

func bounceRequest(t *testing.T, srv *Server, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestBounce_RequestPollConsumeOverHTTP(t *testing.T) {
	srv, token, st := newBounceTestServer(t)

	rec := bounceRequest(t, srv, token, http.MethodPost, "/api/v1/runs/run-1/nodes/build/bounce", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("request status = %d, body %s; want 200", rec.Code, rec.Body.String())
	}
	var created store.NodeBounce
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created row: %v", err)
	}
	if created.RequestedBy != "alice" {
		t.Errorf("requested_by = %q, want the authenticated principal", created.RequestedBy)
	}

	rec = bounceRequest(t, srv, token, http.MethodGet, "/api/v1/runs/run-1/nodes/build/bounce", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("poll status = %d, want 200", rec.Code)
	}
	var polled store.NodeBounce
	if err := json.Unmarshal(rec.Body.Bytes(), &polled); err != nil {
		t.Fatalf("decode polled row: %v", err)
	}
	if polled.Seq != created.Seq {
		t.Errorf("polled seq = %d, want %d", polled.Seq, created.Seq)
	}

	rec = bounceRequest(t, srv, token, http.MethodPost,
		"/api/v1/runs/run-1/nodes/build/bounce/consume", `{"seq":1,"outcome":"bounced"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("consume status = %d, body %s; want 204", rec.Code, rec.Body.String())
	}

	rec = bounceRequest(t, srv, token, http.MethodGet, "/api/v1/runs/run-1/nodes/build/bounce", "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("poll after consume = %d, want 204 (nothing to do is not an error)", rec.Code)
	}

	rows, err := st.ListNodeBounces(context.Background(), "run-1")
	if err != nil || len(rows) != 1 || rows[0].Outcome != store.BounceBounced {
		t.Fatalf("rows = %+v, %v; want one row consumed as bounced", rows, err)
	}
}

func TestBounce_RefusalsCarryTheirStatusAndReason(t *testing.T) {
	srv, token, st := newBounceTestServer(t)

	rec := bounceRequest(t, srv, token, http.MethodPost, "/api/v1/runs/run-9/nodes/build/bounce", "{}")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown run = %d, want 404", rec.Code)
	}
	rec = bounceRequest(t, srv, token, http.MethodPost, "/api/v1/runs/run-1/nodes/deploy/bounce", "{}")
	if rec.Code != http.StatusConflict {
		t.Fatalf("node that never started = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pending") {
		t.Errorf("body = %s, want it to name the status the node is in", rec.Body.String())
	}

	if err := st.FinishRun(context.Background(), "run-1", "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	rec = bounceRequest(t, srv, token, http.MethodPost, "/api/v1/runs/run-1/nodes/build/bounce", "{}")
	if rec.Code != http.StatusConflict {
		t.Errorf("finished run = %d, want 409", rec.Code)
	}
}

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedDispatch(t *testing.T) *store.Store {
	t.Helper()
	st := newStoreForAuth(t)
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.WriteNodeDispatch(ctx, store.NodeDispatch{
		RunID:        "run-1",
		NodeID:       "build",
		Seq:          0,
		EnvJSON:      []byte(`{"SPARKWING_RUN_ID":"run-1"}`),
		RedactedKeys: []byte(`["GITHUB_TOKEN"]`),
	}); err != nil {
		t.Fatalf("WriteNodeDispatch: %v", err)
	}
	return st
}

func dispatchAs(t *testing.T, st *store.Store, path string, scopes ...string) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(contextWithPrincipal(req.Context(),
		&Principal{Name: "reader", Kind: "user", Scopes: scopes}))
	rec := httptest.NewRecorder()
	New(st, nil).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s as %v: status %d body %s", path, scopes, rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

func TestNodeDispatch_EnvJSONNeedsAdmin(t *testing.T) {
	st := seedDispatch(t)
	const path = "/api/v1/runs/run-1/nodes/build/dispatch"

	var reader store.NodeDispatch
	if err := json.Unmarshal(dispatchAs(t, st, path, ScopeRunsRead), &reader); err != nil {
		t.Fatalf("decode reader body: %v", err)
	}
	if len(reader.EnvJSON) != 0 {
		t.Fatalf("runs.read must not see env_json, got %s", string(reader.EnvJSON))
	}
	if string(reader.RedactedKeys) != `["GITHUB_TOKEN"]` {
		t.Fatalf("redacted_keys should survive the strip, got %s", string(reader.RedactedKeys))
	}

	var admin store.NodeDispatch
	if err := json.Unmarshal(dispatchAs(t, st, path, ScopeAdmin), &admin); err != nil {
		t.Fatalf("decode admin body: %v", err)
	}
	if string(admin.EnvJSON) != `{"SPARKWING_RUN_ID":"run-1"}` {
		t.Fatalf("admin should see env_json, got %s", string(admin.EnvJSON))
	}
}

func TestListNodeDispatches_EnvJSONNeedsAdmin(t *testing.T) {
	st := seedDispatch(t)
	const path = "/api/v1/runs/run-1/nodes/build/dispatches"

	var reader []store.NodeDispatch
	if err := json.Unmarshal(dispatchAs(t, st, path, ScopeRunsRead), &reader); err != nil {
		t.Fatalf("decode reader body: %v", err)
	}
	if len(reader) != 1 {
		t.Fatalf("dispatches: got %d, want 1", len(reader))
	}
	if len(reader[0].EnvJSON) != 0 {
		t.Fatalf("runs.read must not see env_json in the list, got %s", string(reader[0].EnvJSON))
	}

	var admin []store.NodeDispatch
	if err := json.Unmarshal(dispatchAs(t, st, path, ScopeAdmin), &admin); err != nil {
		t.Fatalf("decode admin body: %v", err)
	}
	if string(admin[0].EnvJSON) != `{"SPARKWING_RUN_ID":"run-1"}` {
		t.Fatalf("admin should see env_json in the list, got %s", string(admin[0].EnvJSON))
	}
}

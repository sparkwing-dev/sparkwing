package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControllerEventsUseTenantAndReplica(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	s := New(engine.Store, nil)
	if err := s.store.AsOperator().CreateTeam(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	other := New(s.store, nil)
	for _, server := range []*Server{s, other} {
		r := storeDataRequest(t, server, http.MethodPost, "/api/v1/runs/run/events?team=b", `{"kind":"note","payload":"YWJj"}`, Principal{Name: "same-name", Team: "a", Scopes: []string{ScopeAdmin}})
		r.SetPathValue("id", "run")
		response := httptest.NewRecorder()
		server.handleAppendEvent(response, r)
		if response.Code != http.StatusOK {
			t.Fatalf("event=%d: %s", response.Code, response.Body)
		}
		var bytes int64
		if err := s.store.DB().QueryRowContext(t.Context(), "SELECT SUM(bytes) FROM storage_month_usage WHERE team='a' AND replica_id=?", server.leaseHolder).Scan(&bytes); err != nil || bytes != 7 {
			t.Fatalf("controller replica bytes=%d: %v", bytes, err)
		}
	}
	var foreign int64
	if err := s.store.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM storage_month_usage WHERE team='b'").Scan(&foreign); err != nil || foreign != 0 {
		t.Fatalf("foreign charges=%d: %v", foreign, err)
	}
}

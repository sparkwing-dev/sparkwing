package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A refused secret read reaches the caller with the controller's reason, so a
// step reading an undeclared secret learns where to declare it.
func TestGetSecretForRunCarriesTheRefusalMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"secret_undeclared","message":"pipeline \"build\" read secret \"X\", which it does not declare."}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, nil).GetSecretForRun(context.Background(), "X", "run-1")
	if err == nil || err.Error() != `controller 403 (secret_undeclared): pipeline "build" read secret "X", which it does not declare.` {
		t.Fatalf("err = %v, want the refusal message and its code", err)
	}
}

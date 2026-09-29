package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A 5xx is marked as a failure the same ask may get past; a refusal is not.
func TestSourceCredential_MarksOnlyServerFailures(t *testing.T) {
	for status, transient := range map[int]bool{http.StatusBadGateway: true, http.StatusForbidden: false, http.StatusConflict: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"x","message":"y"}`, status)
		}))
		_, err := NewWithToken(srv.URL, nil, "swc_x").SourceCredential(context.Background(), "run-1")
		srv.Close()
		if err == nil || errors.Is(err, ErrControllerFailed) != transient {
			t.Fatalf("%d: err = %v, want ErrControllerFailed %v", status, err, transient)
		}
	}
}

package routepolicy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
)

func TestAbandonedRequestIsAnsweredAsRetryable(t *testing.T) {
	var stop context.CancelFunc
	router := routepolicy.New(routepolicy.Authorization{})
	handle := func(_ http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
		if stop == nil {
			return errors.New("health store failed")
		}
		stop()
		return r.Context().Err()
	}
	if err := router.Register(controllerwire.Endpoint{Method: "GET", Pattern: "/api/v1/health", Authentication: capability.AuthPublic, Handle: handle}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		abandon bool
		status  int
		code    string
	}{
		{"handler failure", false, http.StatusInternalServerError, `"code":"internal"`},
		{"stopping controller", true, http.StatusServiceUnavailable, `"code":"unavailable"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stop = nil
			if tc.abandon {
				stop = cancel
			}
			response := httptest.NewRecorder()
			router.Mux.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/health", nil))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.code) {
				t.Fatalf("response %d %s, want %d with %s", response.Code, response.Body, tc.status, tc.code)
			}
		})
	}
}

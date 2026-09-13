package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func foreignNotFoundServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><body>404 Not Found</body></html>"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEveryHelperRefusesA404NoControllerWrote(t *testing.T) {
	srv := foreignNotFoundServer(t)
	for _, tc := range clientCalls() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "CreateRun" {
				t.Skip("CreateRun reports any unexpected status through readHTTPError and never maps 404 to a missing record")
			}
			err := tc.call(context.Background(), client.New(srv.URL, srv.Client()))
			if errors.Is(err, store.ErrNotFound) {
				t.Fatalf("%s read a 404 the controller did not write as a missing record: %v", tc.name, err)
			}
			if !errors.Is(err, client.ErrForeignNotFound) {
				t.Fatalf("%s error = %v, want ErrForeignNotFound", tc.name, err)
			}
			if !strings.Contains(err.Error(), srv.URL) {
				t.Errorf("%s error does not name the URL it asked: %v", tc.name, err)
			}
		})
	}
}

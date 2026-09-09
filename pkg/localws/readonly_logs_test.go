package localws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func TestBuildHandler_ReadOnlyProtectsLogs(t *testing.T) {
	logServer, err := logs.NewPrivate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, readOnly := range []bool{false, true} {
		handler := buildHandler(ctx, cancel, Options{ReadOnly: readOnly}, handlerParts{logs: logServer}, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("dashboard")}})
		for _, tc := range []struct {
			method, path string
			want         int
		}{
			{http.MethodPost, "/api/v1/logs/run-a/build", http.StatusNoContent},
			{http.MethodGet, "/api/v1/logs/run-a/build", http.StatusOK},
			{http.MethodDelete, "/api/v1/logs/run-a", http.StatusNoContent},
		} {
			req := httptest.NewRequest(tc.method, "http://127.0.0.1"+tc.path, strings.NewReader("hello\n"))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			want := tc.want
			if readOnly && tc.method != http.MethodGet {
				want = http.StatusMethodNotAllowed
			}
			if rec.Code != want {
				t.Errorf("readOnly=%v %s %s: status %d, want %d", readOnly, tc.method, tc.path, rec.Code, want)
			}
		}
	}
}

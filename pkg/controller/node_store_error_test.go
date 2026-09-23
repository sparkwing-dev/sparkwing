package controller

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNodeStoreErrorReportsTransientPostgresContention(t *testing.T) {
	for _, code := range []string{"40001", "40P01", "55P03"} {
		t.Run(code, func(t *testing.T) {
			var logs bytes.Buffer
			s := New(nil, slog.New(slog.NewTextHandler(&logs, nil)))
			w := httptest.NewRecorder()
			s.writeNodeStoreError(w, "acknowledge node execution start", "run-1", "node-1",
				&pgconn.PgError{Code: code, Message: "contention"})
			if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" {
				t.Fatalf("status=%d Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
			}
			if got := logs.String(); !strings.Contains(got, "run_id=run-1") || !strings.Contains(got, "node_id=node-1") || !strings.Contains(got, "contention") {
				t.Fatalf("missing node or error in log: %s", got)
			}
		})
	}
	var logs bytes.Buffer
	s := New(nil, slog.New(slog.NewTextHandler(&logs, nil)))
	w := httptest.NewRecorder()
	s.writeNodeStoreError(w, "touch node heartbeat", "run-1", "node-1", errors.New("broken"))
	if w.Code != http.StatusInternalServerError || w.Header().Get("Retry-After") != "" {
		t.Fatalf("non-transient status=%d Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
	}
}

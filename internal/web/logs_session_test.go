package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// A signed-in browser's durable log reads reach the logs service as that
// browser's session, both through the logs proxy and through the backend's
// log store, so the logs service checks the user's team and not the
// dashboard's own.
func TestDurableLogReadsCarryTheBrowsersSession(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	logsSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = io.WriteString(w, "line\n")
	}))
	t.Cleanup(logsSvc.Close)

	ctx := contextWithWebPrincipal(context.Background(), &sessionResp{
		Principal: "alice", Scopes: []string{"logs.read"}, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, "sess-alice")

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/logs/run-1/n1", nil)
	rec := httptest.NewRecorder()
	logsProxy(HandlerOptions{LogsURL: logsSvc.URL, Token: "service-token", RequireLogin: true}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxied log read = %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := DurableLogStore(logsSvc.URL, "service-token").Read(ctx, "run-1", "n1", storage.ReadOpts{}); err != nil {
		t.Fatalf("durable read: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("logs service saw %d requests, want 2", len(seen))
	}
	for i, auth := range seen {
		if auth != "Session sess-alice" {
			t.Errorf("request %d reached the logs service as %q, want the browser's session", i, auth)
		}
	}
}

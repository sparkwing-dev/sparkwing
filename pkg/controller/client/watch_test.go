package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestHeartbeatsHandWatchReportsToTheObserver(t *testing.T) {
	answer := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(store.LogWatchHeader) != "1" {
			http.Error(w, "the client did not ask", http.StatusBadRequest)
			return
		}
		if answer == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/runs/r1/nodes/n1/heartbeat":
			_, _ = w.Write([]byte(`{"watched":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	var got []WatchReport
	c := New(srv.URL, nil).WithWatchObserver(func(r WatchReport) { got = append(got, r) })
	ctx := context.Background()
	if err := c.HeartbeatNodeClaim(ctx, "r1", "n1", "holder", time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	answer = http.StatusNoContent
	if err := c.HeartbeatNodeClaim(ctx, "r1", "n1", "holder", time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RunID != "r1" || got[0].NodeID != "n1" || !got[0].Watched || got[0].WholeRun {
		t.Fatalf("watch reports = %+v", got)
	}
}

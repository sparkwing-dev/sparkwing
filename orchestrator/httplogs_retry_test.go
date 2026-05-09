package orchestrator_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// 5xx responses must be retried up to the per-line cap; after
// exhausting retries the runner records the drop count + the
// first-seen reason rather than failing the node.
func TestHTTPLogs_5xxRetriesThenCountsDrop(t *testing.T) {
	var posts atomic.Int64
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			if healthy.Load() {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Shrink retry timing so the test isn't gated on real backoff.
	orchestrator.SetTestHTTPNodeLogRetry(t, 3, 1)

	be := orchestrator.NewHTTPLogs(srv.URL, nil, nil)
	nlog, err := be.OpenNodeLog("run-x", "node-x", nil)
	if err != nil {
		t.Fatalf("OpenNodeLog: %v", err)
	}

	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "first"})

	if got := posts.Load(); got != 3 {
		t.Errorf("attempts: got %d POSTs, want 3 (retry budget)", got)
	}

	dropper, ok := nlog.(interface{ Drops() (int, string) })
	if !ok {
		t.Fatalf("nlog %T should expose Drops()", nlog)
	}
	count, reason := dropper.Drops()
	if count != 1 {
		t.Errorf("dropCount: got %d, want 1", count)
	}
	if !strings.Contains(reason, "500") {
		t.Errorf("dropReason should mention HTTP 500, got %q", reason)
	}

	fataler, ok := nlog.(interface{ Fatal() error })
	if !ok {
		t.Fatalf("nlog %T should expose Fatal()", nlog)
	}
	if fataler.Fatal() != nil {
		t.Errorf("Fatal: got %v, want nil (5xx is not auth-fatal)", fataler.Fatal())
	}

	// And once the service recovers, a line lands on the first try
	// and the drop count is unaffected.
	posts.Store(0)
	healthy.Store(true)
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "second"})
	if posts.Load() != 1 {
		t.Errorf("happy path attempts: got %d, want 1", posts.Load())
	}
	if c, _ := dropper.Drops(); c != 1 {
		t.Errorf("dropCount after success: got %d, want 1", c)
	}
}

// A 401/403 response latches Fatal immediately so subsequent emits
// short-circuit. Lets the orchestrator stop wasting cycles once the
// deploy-time auth misconfig is detected.
func TestHTTPLogs_AuthLatchedShortCircuitsLaterEmits(t *testing.T) {
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			http.Error(w, "token lacks required scope: logs.write", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	orchestrator.SetTestHTTPNodeLogRetry(t, 3, 1)

	be := orchestrator.NewHTTPLogs(srv.URL, nil, nil)
	nlog, _ := be.OpenNodeLog("run-x", "node-x", nil)

	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "first"})
	first := posts.Load()
	if first != 1 {
		t.Errorf("first emit: got %d POSTs, want 1 (auth latches before retry budget)", first)
	}

	// Subsequent emits skip the network entirely.
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "second"})
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "third"})
	if posts.Load() != first {
		t.Errorf("after latch: got %d POSTs, want %d (no further attempts)", posts.Load(), first)
	}

	fataler := nlog.(interface{ Fatal() error })
	if fataler.Fatal() == nil {
		t.Errorf("Fatal: got nil, want non-nil after 403")
	}
}

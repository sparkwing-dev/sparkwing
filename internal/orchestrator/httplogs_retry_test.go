package orchestrator_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

	orchestrator.SetTestHTTPNodeLogRetry(t, 3, 1)

	orchestrator.SetTestHTTPNodeLogDropCooldown(t, 0)

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

func TestHTTPLogs_DropCooldownStopsAttemptsButKeepsCounting(t *testing.T) {
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	orchestrator.SetTestHTTPNodeLogRetry(t, 3, 1)
	orchestrator.SetTestHTTPNodeLogDropCooldown(t, 60_000)

	be := orchestrator.NewHTTPLogs(srv.URL, nil, nil)
	nlog, err := be.OpenNodeLog("run-x", "node-x", nil)
	if err != nil {
		t.Fatalf("OpenNodeLog: %v", err)
	}

	for i := range 20 {
		nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: fmt.Sprintf("line-%d", i)})
	}

	if got := posts.Load(); got != 3 {
		t.Errorf("POSTs: got %d, want 3 (only the first line pays the retry budget)", got)
	}
	count, reason := nlog.(interface{ Drops() (int, string) }).Drops()
	if count != 20 {
		t.Errorf("dropCount: got %d, want 20 (every lost line counts, attempted or not)", count)
	}
	if !strings.Contains(reason, "500") {
		t.Errorf("dropReason should mention HTTP 500, got %q", reason)
	}
}

func TestHTTPLogs_DropCooldownExpiryProbesAgain(t *testing.T) {
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

	orchestrator.SetTestHTTPNodeLogRetry(t, 3, 1)
	orchestrator.SetTestHTTPNodeLogDropCooldown(t, 25)

	be := orchestrator.NewHTTPLogs(srv.URL, nil, nil)
	nlog, _ := be.OpenNodeLog("run-x", "node-x", nil)

	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "first"})
	posts.Store(0)
	healthy.Store(true)
	orchestrator.ExpireTestHTTPNodeLogDropCooldown(t, nlog)

	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "second"})
	if got := posts.Load(); got != 1 {
		t.Errorf("post-cooldown POSTs: got %d, want 1 (the window expired, so the line is attempted)", got)
	}
	if c, _ := nlog.(interface{ Drops() (int, string) }).Drops(); c != 1 {
		t.Errorf("dropCount: got %d, want 1 (the recovered line is not a drop)", c)
	}
}

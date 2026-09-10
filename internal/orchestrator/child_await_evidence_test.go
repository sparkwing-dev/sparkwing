package orchestrator_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type awaitEvidencePipe struct{ sparkwing.Base }

func (awaitEvidencePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", func(ctx context.Context) error {
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](ctx,
			"await-evidence-child", "", sparkwing.WithFreshTimeout(300*time.Millisecond))
		return err
	})
	return nil
}

// failingRunReads answers every child run read with 503 while letting the
// parent's own traffic through, which is the store-erroring-for-a-minute
// shape the ticket describes.
func failingRunReads(next http.Handler, parentRunID string) http.Handler {
	prefix := "/api/v1/runs/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix) {
			id := strings.TrimPrefix(r.URL.Path, prefix)
			if id != "" && !strings.HasPrefix(id, parentRunID) {
				http.Error(w, "state store unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func TestRunNodeOnce_ChildAwaitTimeoutNamesWhatTheParentObserved(t *testing.T) {
	if _, ok := sparkwing.Lookup("await-evidence-pipe"); !ok {
		sparkwing.Register[sparkwing.NoInputs]("await-evidence-pipe",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return awaitEvidencePipe{} })
	}
	isolateProfiles(t)
	isolateCheckout(t)
	t.Setenv("SPARKWING_HOME", t.TempDir())

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	var logs syncBuffer
	quiet := slog.New(slog.NewTextHandler(&logs, nil))

	const (
		runID  = "run-await-evidence"
		nodeID = "parent"
	)
	srv := httptest.NewServer(failingRunReads(controller.New(st, quiet).Handler(), runID))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "await-evidence-pipe", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	res, err := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, nodeID,
		"pod:"+runID+":"+nodeID, "", &captureLogger{}, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Failed || res.Err == nil {
		t.Fatalf("outcome = %q, err = %v; want the child await to time out", res.Outcome, res.Err)
	}

	got := res.Err.Error()
	for _, want := range []string{
		"waiting for child",
		"polls=",
		"last_status=none",
		"waited=",
		"store_errors=",
		"first_store_error=",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("timeout error %q is missing %q", got, want)
		}
	}
	if n := strings.Count(logs.String(), "child run status poll failed"); n != 1 {
		t.Errorf("swallowed store errors logged %d times, want exactly 1", n)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

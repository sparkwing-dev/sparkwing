package orchestrator_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type recordingLogStore struct {
	mu      sync.Mutex
	appends [][]byte
}

func (s *recordingLogStore) Append(_ context.Context, _, _ string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appends = append(s.appends, bytes.Clone(data))
	return nil
}

func (s *recordingLogStore) Read(context.Context, string, string, storage.ReadOpts) ([]byte, error) {
	return nil, nil
}
func (s *recordingLogStore) ReadRun(context.Context, string) ([]byte, error) { return nil, nil }
func (s *recordingLogStore) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}
func (s *recordingLogStore) DeleteRun(context.Context, string) error { return nil }

var _ storage.LogStore = (*recordingLogStore)(nil)

type recordingSink struct {
	mu      sync.Mutex
	nodes   []string
	batches [][]byte
}

func (s *recordingSink) AppendNodeLiveLog(_ context.Context, runID, nodeID string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = append(s.nodes, runID+"/"+nodeID)
	s.batches = append(s.batches, bytes.Clone(data))
	return nil
}

func (s *recordingSink) joined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(bytes.Join(s.batches, nil))
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.batches)
}

var _ orchestrator.LiveLogSink = (*recordingSink)(nil)

func TestLiveSink_MirrorsLinesAlongsideTheDurableWrite(t *testing.T) {
	durable := &recordingLogStore{}
	sink := &recordingSink{}
	be := orchestrator.NewLogStoreBackend(durable, nil).WithLiveSink(sink)

	nlog, err := be.OpenNodeLog(context.Background(), "run-1", "build", nil)
	if err != nil {
		t.Fatalf("OpenNodeLog: %v", err)
	}
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "first"})
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "second"})
	if err := nlog.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := sink.joined()
	for _, want := range []string{`"first"`, `"second"`} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("live sink received %q, missing %s", got, want)
		}
	}
	if sink.count() > 2 {
		t.Errorf("live sink took %d posts for 2 lines; the mirror is not batching", sink.count())
	}
	durable.mu.Lock()
	durableAppends := len(durable.appends)
	durable.mu.Unlock()
	if durableAppends == 0 {
		t.Error("mirroring replaced the durable write instead of accompanying it")
	}
}

func TestLiveSink_UnusedWhenTheLogsSurfaceAlreadyStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := &recordingSink{}
	be := orchestrator.NewHTTPLogs(srv.URL, nil, nil).WithLiveSink(sink)
	nlog, err := be.OpenNodeLog(context.Background(), "run-1", "build", nil)
	if err != nil {
		t.Fatalf("OpenNodeLog: %v", err)
	}
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "first"})
	if err := nlog.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sink.count() != 0 {
		t.Fatalf("live sink took %d posts; the logs service already serves the live read", sink.count())
	}
}

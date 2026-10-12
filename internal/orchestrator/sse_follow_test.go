package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestCopySSEStream_PrintsLogLinesWithoutTheFraming(t *testing.T) {
	body := strings.Join([]string{
		": open",
		"",
		"id: 42",
		`data: {"msg":"first"}`,
		"",
		": keepalive",
		"",
		"id: 84",
		`data: {"msg":"second"}`,
		"",
		"event: stream_end",
		"data: {}",
		"",
	}, "\n")

	var out bytes.Buffer
	var multi atomic.Bool
	if ended := copySSEStream(context.Background(), strings.NewReader(body), "build", &multi, &sync.Mutex{}, &out); !ended {
		t.Error("a stream closed by stream_end did not report that it ended")
	}

	got := out.String()
	want := "{\"msg\":\"first\"}\n{\"msg\":\"second\"}\n"
	if got != want {
		t.Fatalf("follow output =\n%q\nwant\n%q", got, want)
	}
	for _, framing := range []string{"id:", "data:", ": open", ": keepalive", "event:"} {
		if strings.Contains(got, framing) {
			t.Errorf("follow output leaked %q framing:\n%s", framing, got)
		}
	}
}

func TestCopySSEStream_PrefixesEveryLineWhenFollowingSeveralNodes(t *testing.T) {
	var out bytes.Buffer
	var multi atomic.Bool
	multi.Store(true)
	copySSEStream(context.Background(), strings.NewReader("data: hello\n\n"), "build", &multi, &sync.Mutex{}, &out)
	if got := out.String(); got != "[build] hello\n" {
		t.Fatalf("multi-node output = %q", got)
	}
}

func TestCopySSEStream_StopsOnACancelledFollow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	var multi atomic.Bool
	copySSEStream(ctx, io.LimitReader(strings.NewReader("data: hello\n\n"), 64), "build", &multi, &sync.Mutex{}, &out)
	if out.Len() != 0 {
		t.Fatalf("cancelled follow still wrote %q", out.String())
	}
}

type resumingLogs struct {
	storage.LogStore
	mu      sync.Mutex
	cursors []string
}

// Stream answers the first open with a line and a cursor, then drops the
// connection; a resume from that cursor gets the next line and the end.
func (l *resumingLogs) Stream(ctx context.Context, _, _ string) (io.ReadCloser, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cursors = append(l.cursors, storage.LogCursor(ctx))
	if storage.LogCursor(ctx) == "" {
		return io.NopCloser(strings.NewReader(": open\n\ndata: line 1\n\nid: c1\n\n")), nil
	}
	return io.NopCloser(strings.NewReader("data: line 2\n\nid: c2\n\nevent: stream_end\ndata: {}\n\n")), nil
}

// A stream that drops mid-node is resumed from the last event id the
// controller sent, so nothing is printed twice, and the follow stops at
// stream_end.
func TestFollowedNode_ResumesFromItsCursorWithoutReplay(t *testing.T) {
	logs := &resumingLogs{}
	var lines []string
	f := &followedNode{done: make(chan struct{})}
	if err := f.follow(context.Background(), logs, "run-1", "a", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, ",") != "line 1,line 2" || strings.Join(logs.cursors, ",") != ",c1" {
		t.Fatalf("printed %q over opens with cursors %q; want each line once, resumed from c1", lines, logs.cursors)
	}
}

// A stream the controller refuses, as it does a token without logs.read,
// ends the follow with that refusal rather than retrying it forever.
func TestFollowLogsRemote_EndsOnARefusedStream(t *testing.T) {
	const runID = "run-refused"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/nodes"):
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []store.Node{{RunID: runID, NodeID: "a", Status: "done"}}})
		case r.URL.Path == "/api/v1/capabilities":
			_, _ = io.WriteString(w, `{"logs":{"segments":true}}`)
		case strings.HasSuffix(r.URL.Path, "/stream"):
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		case r.URL.Path == "/api/v1/runs/"+runID:
			_ = json.NewEncoder(w).Encode(store.Run{ID: runID, Status: "success"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	shortFollowTiming(t, time.Second, 10*time.Millisecond)
	ctrl := client.NewWithToken(srv.URL, nil, "")
	if err := followLogsRemote(context.Background(), ctrl, ctrl.NodeLogs(), runID, "", io.Discard); !errors.Is(err, client.ErrLogStreamRefused) {
		t.Fatalf("follow = %v, want the refusal", err)
	}
}

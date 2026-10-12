package orchestrator

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

type oneStreamLogs struct {
	storage.LogStore
	opened atomic.Int32
}

func (l *oneStreamLogs) Stream(context.Context, string, string) (io.ReadCloser, error) {
	l.opened.Add(1)
	return io.NopCloser(strings.NewReader("data: line 1\n\nevent: stream_end\ndata: {}\n\n")), nil
}

// A node whose log stream ended is sealed while the run goes on, so the
// follow stops there rather than reconnecting and printing it again.
func TestStreamNode_StopsAtTheEndOfASealedNodesLog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	logs := &oneStreamLogs{}
	var out bytes.Buffer
	var multi atomic.Bool
	streamNode(ctx, logs, "run-1", "a", &multi, &sync.Mutex{}, &out)
	if ctx.Err() != nil || logs.opened.Load() != 1 || out.String() != "line 1\n" {
		t.Fatalf("follow opened the stream %d times and wrote %q (ctx %v); want once, line 1 alone",
			logs.opened.Load(), out.String(), ctx.Err())
	}
}

// A follow streams a node again when a retry or a later claim runs it
// under a new attempt after the last attempt's log ended, and not while a
// stream is open, between attempts, or once the node is done.
func TestNodeFollows_StreamsEachAttemptOfANodeOnce(t *testing.T) {
	f := newNodeFollows()
	node := func(status string, consumed int, generation int64) *store.Node {
		return &store.Node{NodeID: "a", Status: status, AttemptsConsumed: consumed, ClaimGeneration: generation}
	}
	for i, step := range []struct {
		node  *store.Node
		ended bool
		want  bool
	}{
		{node("running", 0, 1), false, true},
		{node("running", 0, 1), false, false},
		{node("pending", 1, 1), true, false},
		{node("running", 1, 2), false, true},
		{node("running", 1, 3), true, true},
		{node("done", 2, 3), true, false},
	} {
		if step.ended {
			f.ended("a")
		}
		if got := f.start(step.node); got != step.want {
			t.Fatalf("step %d: start(%+v) = %v, want %v", i, *step.node, got, step.want)
		}
	}
	if f.count() != 1 {
		t.Fatalf("count = %d, want one node", f.count())
	}
}

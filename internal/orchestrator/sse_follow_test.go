package orchestrator

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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

type attemptLogs struct {
	mu     sync.Mutex
	logs   map[logIdentity]string
	opened map[logIdentity]int
}

func (l *attemptLogs) put(id logIdentity, body string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logs[id] = body
}

func (l *attemptLogs) StreamAttempt(_ context.Context, _, nodeID string, attempt, generation int64) (io.ReadCloser, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := logIdentity{node: nodeID, attempt: attempt, generation: generation}
	l.opened[id]++
	body, ok := l.logs[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(strings.NewReader(body + "event: stream_end\ndata: {}\n\n")), nil
}

func newAttemptFollow(logs *attemptLogs, out io.Writer) *attemptFollower {
	return &attemptFollower{logs: logs, runID: "run-1", out: out, state: map[logIdentity]followState{}, nodes: map[string]bool{}}
}

func pollAndWait(t *testing.T, f *attemptFollower, terminal bool, nodes ...*store.Node) bool {
	t.Helper()
	finished := f.poll(context.Background(), nodes, "", terminal)
	f.wg.Wait()
	return finished
}

func attemptOf(attempt int, generation int64) store.ExecutionAttempt {
	return store.ExecutionAttempt{Attempt: attempt, ClaimGeneration: generation}
}

// A replacement claim seen running before its log exists is asked for
// again at the next poll, never answered with the earlier claim's log, and
// its own log is streamed once it appears.
func TestAttemptFollower_WaitsForAReplacementClaimsOwnLog(t *testing.T) {
	first, second := logIdentity{"a", 1, 1}, logIdentity{"a", 1, 2}
	logs := &attemptLogs{logs: map[logIdentity]string{first: "data: first claim\n\n"}, opened: map[logIdentity]int{}}
	var out bytes.Buffer
	f := newAttemptFollow(logs, &out)
	reclaimed := &store.Node{
		NodeID: "a", Status: "running", ClaimGeneration: 2,
		ExecutionAttempts: []store.ExecutionAttempt{attemptOf(1, 1), attemptOf(1, 2)},
	}

	pollAndWait(t, f, false, reclaimed)
	pollAndWait(t, f, false, reclaimed)
	if logs.opened[second] == 0 || out.String() != "first claim\n" {
		t.Fatalf("before the new claim's log exists: opened %v, wrote %q", logs.opened, out.String())
	}
	logs.put(second, "data: second claim\n\n")
	pollAndWait(t, f, false, reclaimed)
	if finished := pollAndWait(t, f, true, reclaimed); !finished {
		t.Fatal("follow not finished with the run terminal and both claims streamed")
	}
	if out.String() != "first claim\nsecond claim\n" || logs.opened[first] != 1 {
		t.Fatalf("wrote %q, opened %v; want each claim's log once, in order", out.String(), logs.opened)
	}
}

// A retry that started and finished between two polls is first seen done,
// and its log is still streamed; an attempt that never got a log of its
// own, as an in-process retry does not, ends the follow once the run is
// terminal.
func TestAttemptFollower_StreamsARetryFirstSeenDone(t *testing.T) {
	logs := &attemptLogs{logs: map[logIdentity]string{
		{"a", 1, 1}: "data: attempt 1\n\n",
		{"a", 2, 2}: "data: attempt 2\n\n",
	}, opened: map[logIdentity]int{}}
	var out bytes.Buffer
	f := newAttemptFollow(logs, &out)

	pollAndWait(t, f, false, &store.Node{
		NodeID: "a", Status: "running", ClaimGeneration: 1,
		ExecutionAttempts: []store.ExecutionAttempt{attemptOf(1, 1)},
	})
	done := &store.Node{
		NodeID: "a", Status: "done", AttemptsConsumed: 3, ClaimGeneration: 2,
		ExecutionAttempts: []store.ExecutionAttempt{attemptOf(1, 1), attemptOf(2, 2), attemptOf(3, 2)},
	}
	if finished := pollAndWait(t, f, true, done); finished {
		t.Fatal("follow finished before the retry's log was streamed")
	}
	for range 3 {
		if pollAndWait(t, f, true, done) {
			break
		}
	}
	if !pollAndWait(t, f, true, done) {
		t.Fatalf("follow never finished; states %v", f.state)
	}
	if out.String() != "attempt 1\nattempt 2\n" {
		t.Fatalf("wrote %q, want both attempts once", out.String())
	}
}

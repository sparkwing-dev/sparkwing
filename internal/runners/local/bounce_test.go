//go:build unix

package local

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// bounceScript is a fake pipeline binary that records one line per
// attempt. The first attempt blocks, so the process an operator
// bounces is still alive when the kill arrives; every later attempt
// blocks until the test releases it, so the replacement is provably
// still running while the test inspects the node row.
//
// The replacement waiting is the whole point. A replacement that
// exited on its own would reach a terminal row of its own before the
// assertion could look, and the test would report the invariant broken
// when what actually happened is that the next attempt finished.
func bounceScript(t *testing.T) (script, attempts, release string) {
	t.Helper()
	dir := t.TempDir()
	attempts = filepath.Join(dir, "attempts")
	release = filepath.Join(dir, "release")
	return "echo $$ >> " + attempts + "\n" +
		"if [ \"$(wc -l < " + attempts + ")\" -eq 1 ]; then sleep 60; fi\n" +
		// safety: bounded, so a failing test cannot leave a process behind
		// waiting for a release file nobody is going to write.
		"i=0\n" +
		"while [ ! -f " + release + " ] && [ $i -lt 600 ]; do sleep 0.1; i=$((i+1)); done\n" +
		"exit 0", attempts, release
}

// releaseAttempt lets a waiting replacement attempt exit, so the test
// finishes in milliseconds rather than waiting out a timeout.
func releaseAttempt(t *testing.T, release string) {
	t.Helper()
	if err := os.WriteFile(release, []byte("go\n"), 0o644); err != nil {
		t.Fatalf("release the waiting attempt: %v", err)
	}
}

// attemptPIDs returns the pid of every attempt recorded so far.
func attemptPIDs(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}

// waitForAttempts blocks until at least n attempts have been recorded.
func waitForAttempts(t *testing.T, path string, n int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if pids := attemptPIDs(t, path); len(pids) >= n {
			return pids
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d attempt(s) recorded within %s, want %d",
				len(attemptPIDs(t, path)), timeout, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newBlockedReadFixture returns a Runner talking to the fixture's
// controller through a proxy that fails node reads and passes
// everything else through -- one loopback going deaf on one route,
// which is the shape of the hiccup the runner must not misread.
func newBlockedReadFixture(t *testing.T, f *spawnFixture) *Runner {
	t.Helper()
	upstream, err := url.Parse(f.url)
	if err != nil {
		t.Fatalf("parse controller url: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/nodes/build") {
			http.Error(w, "controller unavailable", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(client.NewWithToken(srv.URL, nil, ""), Config{
		Executable:        f.runner.cfg.Executable,
		ControllerURL:     srv.URL,
		WorkDir:           t.TempDir(),
		Home:              t.TempDir(),
		TerminationGrace:  time.Second,
		SuperviseInterval: 25 * time.Millisecond,
		Logger:            quiet,
	})
}

func nodeEvents(t *testing.T, f *spawnFixture, runID, kind string) []store.Event {
	t.Helper()
	events, err := f.store.ListEventsAfter(context.Background(), runID, 0, 500)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var out []store.Event
	for _, ev := range events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// A bounce kills the node's process and runs the node again, and the
// node row never goes terminal in between. That absence is the whole
// point: a terminal row would wake the dispatcher's waiters, and a
// failed one would cascade into every downstream node -- for what an
// operator asked to be a restart.
func TestRunNode_BounceKillsTheProcessAndRunsTheNodeAgain(t *testing.T) {
	script, attempts, release := bounceScript(t)
	f := newSpawnFixture(t, fakeNodeBinary(t, script))
	f.seedNode(t, "run-b1", "build")
	ctx := context.Background()
	if err := f.store.StartNode(ctx, "run-b1", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}

	done := make(chan runner.Result, 1)
	go func() { done <- f.runner.RunNode(ctx, runner.Request{RunID: "run-b1", NodeID: "build"}) }()

	waitForAttempts(t, attempts, 1, 15*time.Second)
	// safety: the first attempt has to be measurably alive, so the accumulated
	// wall time below cannot be explained by the replacement alone.
	time.Sleep(200 * time.Millisecond)
	if _, err := f.store.RequestNodeBounce(ctx, "run-b1", "build", "korey"); err != nil {
		t.Fatalf("RequestNodeBounce: %v", err)
	}

	pids := waitForAttempts(t, attempts, 2, 15*time.Second)
	// The replacement is live -- it waits for the release file below -- and
	// the killed attempt is reaped, so this is the moment a terminal row
	// would have to exist if one were written.
	mid, err := f.store.GetNode(ctx, "run-b1", "build")
	if err != nil {
		t.Fatalf("get node mid-bounce: %v", err)
	}
	if mid.Outcome != "" || mid.Status == "done" {
		t.Errorf("node went terminal across the bounce (%s/%s); downstream would have cascaded",
			mid.Status, mid.Outcome)
	}
	releaseAttempt(t, release)

	var res runner.Result
	select {
	case res = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunNode never returned after the bounce")
	}
	if pids[0] == pids[1] {
		t.Errorf("both attempts report pid %s; the node was not re-run in a fresh process", pids[0])
	}

	rows, err := f.store.ListNodeBounces(ctx, "run-b1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListNodeBounces = %d rows, %v", len(rows), err)
	}
	if rows[0].ConsumedAt == nil || rows[0].Outcome != store.BounceBounced {
		t.Errorf("request row = %+v, want it consumed as %q", rows[0], store.BounceBounced)
	}

	evs := nodeEvents(t, f, "run-b1", "node_bounced")
	if len(evs) != 1 {
		t.Fatalf("node_bounced events = %d, want 1", len(evs))
	}
	var attrs map[string]any
	if err := json.Unmarshal(evs[0].Payload, &attrs); err != nil {
		t.Fatalf("decode node_bounced payload: %v", err)
	}
	if attrs["requested_by"] != "korey" {
		t.Errorf("event attrs = %v, want the requester recorded", attrs)
	}
	if attrs["admission_lease_retained"] != true {
		t.Errorf("event attrs = %v, want admission_lease_retained: the work was already admitted", attrs)
	}

	if res.Usage == nil {
		t.Fatal("Usage is nil; the killed attempt's accounting was dropped")
	}
	if res.Usage.Wall < 200*time.Millisecond {
		t.Errorf("Usage.Wall = %s, want both attempts' spans; the killed one lived at least 200ms",
			res.Usage.Wall)
	}
}

// A node that finishes before the kill lands is a no-op. The terminal
// row wins -- it is the authority on how the work went -- so the
// request is closed as missed and nothing is re-run.
//
// The row is terminal from the start here rather than mid-flight, which
// is the same state the runner reads at kill time and makes the race
// deterministic instead of a sleep race.
func TestRunNode_BounceOfANodeThatAlreadyFinishedIsANoOp(t *testing.T) {
	script, attempts, _ := bounceScript(t)
	f := newSpawnFixture(t, fakeNodeBinary(t, script))
	f.seedNode(t, "run-b2", "build")
	ctx := context.Background()
	if err := f.store.StartNode(ctx, "run-b2", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}
	if _, err := f.store.RequestNodeBounce(ctx, "run-b2", "build", "korey"); err != nil {
		t.Fatalf("RequestNodeBounce: %v", err)
	}
	if err := f.store.FinishNode(ctx, "run-b2", "build",
		string(sparkwing.Success), "", []byte(`{"digest":"abc"}`)); err != nil {
		t.Fatalf("finish node: %v", err)
	}

	res := f.runner.RunNode(ctx, runner.Request{RunID: "run-b2", NodeID: "build"})
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v), want the terminal row's success", res.Outcome, res.Err)
	}
	// safety: at most one, not exactly one. The kill can land before the
	// first attempt's shell reaches its own echo, which is a fast
	// machine rather than a wrong outcome; a second attempt is the
	// failure -- a finished node has nothing to re-run.
	if pids := attemptPIDs(t, attempts); len(pids) > 1 {
		t.Errorf("attempts = %d, want no re-run of a node that already finished", len(pids))
	}

	rows, err := f.store.ListNodeBounces(ctx, "run-b2")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListNodeBounces = %d rows, %v", len(rows), err)
	}
	if rows[0].ConsumedAt == nil || rows[0].Outcome != store.BounceMissed {
		t.Errorf("request row = %+v, want it consumed as %q", rows[0], store.BounceMissed)
	}
	if len(nodeEvents(t, f, "run-b2", "node_bounce_missed")) != 1 {
		t.Error("no node_bounce_missed event; the request's fate is not in the run's history")
	}
	if len(nodeEvents(t, f, "run-b2", "node_bounced")) != 0 {
		t.Error("a node_bounce_missed request was also recorded as a bounce")
	}
}

// A request the node outran is closed when the node ends, not left
// open.
//
// The kill was never delivered -- the node reached its own outcome
// before the poll came round -- so the request is a miss. Leaving it
// pending would be worse than untidy: nothing distinguishes a stale
// row from a fresh one, and the next thing to run this node id would
// read it as an instruction.
//
// The long supervision interval is what makes this deterministic: the
// poll provably cannot fire inside the node's life, so the request is
// still open when the node ends, every time.
func TestRunNode_RequestTheNodeOutranIsConsumedAsMissed(t *testing.T) {
	f := newSpawnFixture(t, fakeNodeBinary(t, "exit 0"), func(c *Config) {
		c.SuperviseInterval = time.Hour
	})
	f.seedNode(t, "run-b4", "build")
	ctx := context.Background()
	if err := f.store.StartNode(ctx, "run-b4", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}
	if _, err := f.store.RequestNodeBounce(ctx, "run-b4", "build", "korey"); err != nil {
		t.Fatalf("RequestNodeBounce: %v", err)
	}

	f.runner.RunNode(ctx, runner.Request{RunID: "run-b4", NodeID: "build"})

	pending, err := f.store.PendingNodeBounce(ctx, "run-b4", "build")
	if err != nil {
		t.Fatalf("PendingNodeBounce: %v", err)
	}
	if pending != nil {
		t.Fatalf("request %+v is still open after the node ended; the next dispatch would act on it", pending)
	}
	rows, err := f.store.ListNodeBounces(ctx, "run-b4")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListNodeBounces = %+v, %v", rows, err)
	}
	if rows[0].Outcome != store.BounceMissed {
		t.Errorf("outcome = %q, want %q: the kill was never delivered", rows[0].Outcome, store.BounceMissed)
	}
	if len(nodeEvents(t, f, "run-b4", "node_bounce_missed")) == 0 {
		t.Error("no node_bounce_missed event; the request's fate is not in the run's history")
	}
}

// The sweep closes a request that was handed to an attempt but never
// settled, and closes it with the verdict that attempt reached.
//
// Two real paths land here. The attempt's select can take the
// process's own exit while a handed request sits in the channel, and a
// consume can fail after the runner has already acted. In the first
// the runner reached no verdict, so the request is a miss; in the
// second it did, and the ledger is what keeps the sweep from
// relabelling an honored request as one that never landed.
func TestSettleOpenBounces_ClosesWhatAnAttemptLeftOpen(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		ledger    func(seq int64) bounceLedger
		want      string
		wantEvent string
	}{
		{
			name:      "handed but no verdict reached",
			ledger:    func(int64) bounceLedger { return bounceLedger{} },
			want:      store.BounceMissed,
			wantEvent: "node_bounce_missed",
		},
		{
			name:      "verdict reached but the consume was lost",
			ledger:    func(seq int64) bounceLedger { return bounceLedger{seq: store.BounceBounced} },
			want:      store.BounceBounced,
			wantEvent: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFixture(t, fakeNodeBinary(t, "exit 0"))
			f.seedNode(t, "run-sweep", "build")
			if err := f.store.StartNode(ctx, "run-sweep", "build"); err != nil {
				t.Fatalf("start node: %v", err)
			}
			b, err := f.store.RequestNodeBounce(ctx, "run-sweep", "build", "korey")
			if err != nil {
				t.Fatalf("RequestNodeBounce: %v", err)
			}

			f.runner.settleOpenBounces(ctx,
				runner.Request{RunID: "run-sweep", NodeID: "build"}, tc.ledger(b.Seq))

			rows, err := f.store.ListNodeBounces(ctx, "run-sweep")
			if err != nil || len(rows) != 1 {
				t.Fatalf("ListNodeBounces = %+v, %v", rows, err)
			}
			if rows[0].ConsumedAt == nil || rows[0].Outcome != tc.want {
				t.Errorf("row = %+v, want it consumed as %q", rows[0], tc.want)
			}
			missed := len(nodeEvents(t, f, "run-sweep", "node_bounce_missed"))
			if tc.wantEvent == "node_bounce_missed" && missed != 1 {
				t.Errorf("node_bounce_missed events = %d, want 1", missed)
			}
			if tc.wantEvent == "" && missed != 0 {
				t.Errorf("node_bounce_missed events = %d on an honored request, want 0", missed)
			}
		})
	}
}

// A second dispatch of the same node -- an auto-retry is exactly that
// -- must not inherit the previous dispatch's requests.
//
// The poll is per node id, not per attempt, so a request left open by
// the dispatch before it reads to the next one as a fresh instruction.
// This asserts the retry runs its attempt to completion and that no
// bounce is recorded against a dispatch nobody bounced.
func TestRunNode_ARetryDispatchInheritsNoBounce(t *testing.T) {
	dir := t.TempDir()
	attempts := filepath.Join(dir, "attempts")
	wait := filepath.Join(dir, "wait")
	release := filepath.Join(dir, "release")
	// The first dispatch exits at once; the retry's attempt waits, so it
	// is alive across many poll ticks. A stale request would have every
	// opportunity to be picked up and would show as a second attempt.
	f := newSpawnFixture(t, fakeNodeBinary(t,
		"echo $$ >> "+attempts+"\n"+
			"if [ -f "+wait+" ]; then\n"+
			"  i=0\n"+
			"  while [ ! -f "+release+" ] && [ $i -lt 600 ]; do sleep 0.1; i=$((i+1)); done\n"+
			"fi\n"+
			"exit 0"))
	f.seedNode(t, "run-b5", "build")
	ctx := context.Background()
	if err := f.store.StartNode(ctx, "run-b5", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}
	if _, err := f.store.RequestNodeBounce(ctx, "run-b5", "build", "korey"); err != nil {
		t.Fatalf("RequestNodeBounce: %v", err)
	}
	// safety: terminal before the first dispatch, so that dispatch cannot
	// honor the request -- it settles it as a miss, which is the state
	// the retry below must not be able to act on.
	if err := f.store.FinishNode(ctx, "run-b5", "build",
		string(sparkwing.Success), "", nil); err != nil {
		t.Fatalf("finish node: %v", err)
	}
	f.runner.RunNode(ctx, runner.Request{RunID: "run-b5", NodeID: "build"})
	firstDispatch := len(attemptPIDs(t, attempts))

	// The retry: the node is running again under the same id.
	if err := f.store.StartNode(ctx, "run-b5", "build"); err != nil {
		t.Fatalf("restart node: %v", err)
	}
	if err := os.WriteFile(wait, []byte("hold\n"), 0o644); err != nil {
		t.Fatalf("arm the retry's wait: %v", err)
	}
	done := make(chan runner.Result, 1)
	go func() { done <- f.runner.RunNode(ctx, runner.Request{RunID: "run-b5", NodeID: "build"}) }()
	waitForAttempts(t, attempts, firstDispatch+1, 15*time.Second)
	// safety: many supervision ticks (25ms apart) while the retry's process
	// is alive. A stale request would have been handed over inside this
	// window, so passing it is evidence rather than luck.
	time.Sleep(500 * time.Millisecond)
	if retried := len(attemptPIDs(t, attempts)) - firstDispatch; retried != 1 {
		t.Errorf("the retry ran %d attempt(s) so far, want exactly 1: a stale request killed one of them", retried)
	}
	releaseAttempt(t, release)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the retry dispatch never returned")
	}

	if retried := len(attemptPIDs(t, attempts)) - firstDispatch; retried != 1 {
		t.Errorf("the retry ran %d attempt(s), want exactly 1", retried)
	}
	if evs := nodeEvents(t, f, "run-b5", "node_bounced"); len(evs) != 0 {
		t.Errorf("%d node_bounced event(s) on a dispatch nobody bounced", len(evs))
	}
	pending, err := f.store.PendingNodeBounce(ctx, "run-b5", "build")
	if err != nil || pending != nil {
		t.Errorf("pending after both dispatches = %v (%v), want none", pending, err)
	}
}

// When the node's state cannot be read after the kill, the node is not
// re-run.
//
// The read is what tells "killed live work" from "the node had already
// finished", and guessing the first is not free: it re-executes the
// node's steps, potentially over a node that had already succeeded.
// The runner retries the read, and if it still cannot answer it
// declines to re-run and closes the request saying why.
func TestSettleBounce_DoesNotRespawnWhenTheNodeCannotBeRead(t *testing.T) {
	f := newSpawnFixture(t, fakeNodeBinary(t, "exit 0"))
	f.seedNode(t, "run-b6", "build")
	ctx := context.Background()
	if err := f.store.StartNode(ctx, "run-b6", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}
	b, err := f.store.RequestNodeBounce(ctx, "run-b6", "build", "korey")
	if err != nil {
		t.Fatalf("RequestNodeBounce: %v", err)
	}

	// safety: a controller that refuses reads but accepts writes, which is
	// what a loopback hiccup looks like from here -- the runner must
	// not read "no answer" as "the node is still live".
	blocked := newBlockedReadFixture(t, f)
	verdict := blocked.settleBounce(ctx,
		runner.Request{RunID: "run-b6", NodeID: "build"}, b, bounceLedger{}, false)
	if verdict == bounceRespawn {
		t.Fatal("the node was re-run on a read that never answered")
	}

	rows, err := f.store.ListNodeBounces(ctx, "run-b6")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListNodeBounces = %+v, %v", rows, err)
	}
	if rows[0].ConsumedAt == nil {
		t.Error("request left open after the runner declined to act on it")
	}
	evs := nodeEvents(t, f, "run-b6", "node_bounce_missed")
	if len(evs) != 1 {
		t.Fatalf("node_bounce_missed events = %d, want 1", len(evs))
	}
	var attrs map[string]any
	if err := json.Unmarshal(evs[0].Payload, &attrs); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if reason, _ := attrs["reason"].(string); !strings.Contains(reason, "could not be read") {
		t.Errorf("reason = %q, want it to say the state could not be read", reason)
	}
}

// A controller that stops answering the bounce poll must not stall the
// heartbeat sharing that loop.
//
// The two calls are on one goroutine, so an unbounded poll would hold
// the heartbeat for the client's full 30-second timeout. A node whose
// heartbeat goes stale past the reconciler's threshold is reaped as
// abandoned and its run fails -- so an operator convenience with no
// deadline on it could take down the run it exists to save.
func TestSupervise_AHungBouncePollDoesNotStallTheHeartbeat(t *testing.T) {
	f := newSpawnFixture(t, fakeNodeBinary(t, "exit 0"))
	ctx := context.Background()
	f.seedNode(t, "run-b7", "build")
	if err := f.store.StartNode(ctx, "run-b7", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}

	upstream, err := url.Parse(f.url)
	if err != nil {
		t.Fatalf("parse controller url: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var mu sync.Mutex
	var beats []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/bounce") {
			// safety: far longer than any deadline the loop should allow, so a
			// poll that is not bounded here shows up as missing heartbeats.
			select {
			case <-time.After(30 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/touch") {
			mu.Lock()
			beats = append(beats, time.Now())
			mu.Unlock()
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	const interval = 100 * time.Millisecond
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := New(client.NewWithToken(srv.URL, nil, ""), Config{
		Executable:        f.runner.cfg.Executable,
		ControllerURL:     srv.URL,
		WorkDir:           t.TempDir(),
		Home:              t.TempDir(),
		SuperviseInterval: interval,
		Logger:            quiet,
	})

	const window = 5 * time.Second
	superviseCtx, stop := context.WithTimeout(ctx, window)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.supervise(superviseCtx, runner.Request{RunID: "run-b7", NodeID: "build"},
			make(chan *store.NodeBounce, 1))
	}()
	<-done

	mu.Lock()
	defer mu.Unlock()
	// Each round costs one bouncePollTimeout, so the window holds several
	// heartbeats. Unbounded, it would hold the first and one more.
	if len(beats) < 3 {
		t.Fatalf("%d heartbeats in %s; the poll is holding the loop", len(beats), window)
	}
	worst := time.Duration(0)
	for i := 1; i < len(beats); i++ {
		if gap := beats[i].Sub(beats[i-1]); gap > worst {
			worst = gap
		}
	}
	if limit := bouncePollTimeout + interval + time.Second; worst > limit {
		t.Errorf("longest gap between heartbeats = %s, want at most %s (poll timeout + interval)",
			worst, limit)
	}
}

// A bounce that lands while the run is being torn down restarts
// nothing: there is no run left to restart the node for. The request is
// still consumed, so it cannot be waiting for the next process that
// happens to claim this node.
func TestSettleBounce_DuringTeardownRestartsNothing(t *testing.T) {
	f := newSpawnFixture(t, fakeNodeBinary(t, "exit 0"))
	f.seedNode(t, "run-b3", "build")
	ctx := context.Background()
	if err := f.store.StartNode(ctx, "run-b3", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}
	b, err := f.store.RequestNodeBounce(ctx, "run-b3", "build", "korey")
	if err != nil {
		t.Fatalf("RequestNodeBounce: %v", err)
	}

	verdict := f.runner.settleBounce(ctx,
		runner.Request{RunID: "run-b3", NodeID: "build"}, b, bounceLedger{}, true)
	if verdict != bounceTornDown {
		t.Errorf("verdict = %v, want bounceTornDown", verdict)
	}

	pending, err := f.store.PendingNodeBounce(ctx, "run-b3", "build")
	if err != nil || pending != nil {
		t.Errorf("pending after teardown = %v (%v), want the request consumed", pending, err)
	}
	if len(nodeEvents(t, f, "run-b3", "node_bounce_missed")) != 1 {
		t.Error("no node_bounce_missed event for the request teardown swallowed")
	}
}

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

func bounceScript(t *testing.T) (script, attempts, release string) {
	t.Helper()
	dir := t.TempDir()
	attempts = filepath.Join(dir, "attempts")
	release = filepath.Join(dir, "release")
	return "echo $$ >> " + attempts + "\n" +
		"if [ \"$(wc -l < " + attempts + ")\" -eq 1 ]; then sleep 60; fi\n" +

		"i=0\n" +
		"while [ ! -f " + release + " ] && [ $i -lt 600 ]; do sleep 0.1; i=$((i+1)); done\n" +
		"exit 0", attempts, release
}

func releaseAttempt(t *testing.T, release string) {
	t.Helper()
	if err := os.WriteFile(release, []byte("go\n"), 0o644); err != nil {
		t.Fatalf("release the waiting attempt: %v", err)
	}
}

func attemptPIDs(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}

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

	time.Sleep(200 * time.Millisecond)
	if _, err := f.store.RequestNodeBounce(ctx, "run-b1", "build", "korey"); err != nil {
		t.Fatalf("RequestNodeBounce: %v", err)
	}

	pids := waitForAttempts(t, attempts, 2, 15*time.Second)

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

func TestRunNode_ARetryDispatchInheritsNoBounce(t *testing.T) {
	dir := t.TempDir()
	attempts := filepath.Join(dir, "attempts")
	wait := filepath.Join(dir, "wait")
	release := filepath.Join(dir, "release")

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

	if err := f.store.FinishNode(ctx, "run-b5", "build",
		string(sparkwing.Success), "", nil); err != nil {
		t.Fatalf("finish node: %v", err)
	}
	f.runner.RunNode(ctx, runner.Request{RunID: "run-b5", NodeID: "build"})
	firstDispatch := len(attemptPIDs(t, attempts))

	if err := f.store.StartNode(ctx, "run-b5", "build"); err != nil {
		t.Fatalf("restart node: %v", err)
	}
	if err := os.WriteFile(wait, []byte("hold\n"), 0o644); err != nil {
		t.Fatalf("arm the retry's wait: %v", err)
	}
	done := make(chan runner.Result, 1)
	go func() { done <- f.runner.RunNode(ctx, runner.Request{RunID: "run-b5", NodeID: "build"}) }()
	waitForAttempts(t, attempts, firstDispatch+1, 15*time.Second)

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

package nodehost

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type debugStore struct {
	*store.Store
	paused chan string
	asked  chan string

	mu     sync.Mutex
	events []string
}

func (d *debugStore) CreateDebugPause(ctx context.Context, p store.DebugPause) error {
	err := d.Store.CreateDebugPause(ctx, p)
	if err == nil {
		d.paused <- p.NodeID + ":" + p.Reason
	}
	return err
}

func (d *debugStore) CreateApproval(ctx context.Context, a store.Approval) error {
	err := d.Store.CreateApproval(ctx, a)
	if err == nil {
		d.asked <- a.NodeID
	}
	return err
}

func (d *debugStore) AppendEvent(_ context.Context, _, nodeID, kind string, _ []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, nodeID+":"+kind)
	return nil
}

// hack: the lifecycle events every node writes are events_test.go's, so these tests read only the debug ones.
func (d *debugStore) eventList() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(d.events), func(e string) bool {
		_, kind, _ := strings.Cut(e, ":")
		return slices.Contains([]string{"node_started", "node_succeeded", "node_failed", "node_cancelled", "node_skipped"}, kind)
	})
}

func fastDebugPolls(t *testing.T) {
	prevPause, prevBounce := debugPausePoll, bouncePoll
	debugPausePoll, bouncePoll = time.Millisecond, time.Millisecond
	t.Cleanup(func() { debugPausePoll, bouncePoll = prevPause, prevBounce })
}

func hostDebug(t *testing.T, ctx context.Context, ds *debugStore, doc nodeproto.PlanDoc, dbg Debug, exec Executor) RunResult {
	t.Helper()
	if err := ds.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := validateNodes(doc.Nodes); err != nil {
		t.Fatalf("validate: %v", err)
	}
	cfg := Config{Execute: exec, Debug: dbg}
	run := &Run{ID: testRunID, Doc: doc, State: ds, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest)}
	res := RunResult{RunID: testRunID, Nodes: map[string]Result{}}
	s := &scheduler{run: run, results: res.Nodes, started: map[string]bool{}, exec: chain(&cfg)}
	if err := s.add(ctx, ctx, doc.Nodes, nil); err != nil {
		t.Fatalf("add: %v", err)
	}
	s.loop(ctx)
	res.Status, res.Err = s.verdict(ctx)
	return res
}

func newDebugStore(t *testing.T) *debugStore {
	return &debugStore{Store: openStore(t), paused: make(chan string, 8), asked: make(chan string, 8)}
}

func TestDebugPausesHoldTheNodeUntilReleased(t *testing.T) {
	fastDebugPolls(t)
	cases := map[string]struct {
		dbg     Debug
		fail    bool
		reason  string
		ranOnce bool
		outcome sparkwing.Outcome
	}{
		"before":     {dbg: Debug{PauseBefore: []string{"build"}}, reason: store.PauseReasonBefore, outcome: sparkwing.Success},
		"after":      {dbg: Debug{PauseAfter: []string{"build"}}, reason: store.PauseReasonAfter, ranOnce: true, outcome: sparkwing.Success},
		"on failure": {dbg: Debug{PauseOnFailure: true}, fail: true, reason: store.PauseReasonOnFailure, ranOnce: true, outcome: sparkwing.Failed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ds := newDebugStore(t)
			fx := &fakeExec{bodies: map[string]Executor{"build": func(context.Context, *NodeRun) Result {
				if tc.fail {
					return Result{Outcome: sparkwing.Failed, Err: errors.New("broke")}
				}
				return Result{Outcome: sparkwing.Success}
			}}}
			doc := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{{ID: "build"}, {ID: "deploy", Deps: deps("build"), Modifiers: &nodeproto.PlanModifiers{Optional: true}}}}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			done := make(chan RunResult, 1)
			go func() { done <- hostDebug(t, ctx, ds, doc, tc.dbg, fx.run) }()
			if got := <-ds.paused; got != "build:"+tc.reason {
				t.Fatalf("paused %q, want build:%s", got, tc.reason)
			}
			if fx.ran("build") != tc.ranOnce {
				t.Errorf("build ran before release = %v, want %v", fx.ran("build"), tc.ranOnce)
			}
			if fx.ran("deploy") {
				t.Error("deploy ran while build was paused")
			}
			if row := nodeRow(t, ds.Store, "build"); row.Status != sparkwing.Paused {
				t.Errorf("build row status %q while paused, want paused", row.Status)
			}
			if err := ds.ReleaseDebugPause(ctx, testRunID, "build", "alice", store.PauseReleaseManual); err != nil {
				t.Fatal(err)
			}
			<-done
			wantRow(t, ds.Store, "build", tc.outcome, "")
			if !fx.ran("deploy") && tc.outcome == sparkwing.Success {
				t.Error("deploy never ran after build resumed")
			}
			if got, want := ds.eventList(), []string{"build:node_paused", "build:node_resumed"}; !slices.Equal(got, want) {
				t.Errorf("events %v, want %v", got, want)
			}
		})
	}
}

func TestDebugPauseExpiresOnItsTimeout(t *testing.T) {
	fastDebugPolls(t)
	ds := newDebugStore(t)
	fx := &fakeExec{}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	doc := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{{ID: "build"}}}
	res := hostDebug(t, ctx, ds, doc, Debug{PauseBefore: []string{"build"}, PauseTimeout: time.Millisecond}, fx.run)

	if res.Status != "success" || !fx.ran("build") {
		t.Errorf("status %q ran=%v, want success after the pause expired", res.Status, fx.ran("build"))
	}
	pauses, err := ds.ListDebugPauses(ctx, testRunID)
	if err != nil || len(pauses) != 1 || pauses[0].ReleaseKind != store.PauseReleaseTimeout {
		t.Errorf("pauses %+v err %v, want one released by timeout", pauses, err)
	}
}

func TestDebugCancelDuringAPauseCancelsTheNode(t *testing.T) {
	fastDebugPolls(t)
	for _, kind := range []string{store.PauseReasonBefore, store.PauseReasonAfter} {
		t.Run(kind, func(t *testing.T) {
			ds := newDebugStore(t)
			fx := &fakeExec{}
			dbg := Debug{PauseBefore: []string{"build"}}
			if kind == store.PauseReasonAfter {
				dbg = Debug{PauseAfter: []string{"build"}}
			}
			doc := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{{ID: "build"}, {ID: "deploy", Deps: deps("build")}}}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			runCtx, stop := context.WithCancel(ctx)
			done := make(chan RunResult, 1)
			go func() { done <- hostDebug(t, runCtx, ds, doc, dbg, fx.run) }()
			<-ds.paused
			stop()
			res := <-done

			if res.Status != "failed" {
				t.Errorf("run status %q, want failed", res.Status)
			}
			wantRow(t, ds.Store, "build", sparkwing.Cancelled, "ctx-cancelled")
			if fx.ran("build") != (kind == store.PauseReasonAfter) || fx.ran("deploy") {
				t.Errorf("ran build=%v deploy=%v", fx.ran("build"), fx.ran("deploy"))
			}
		})
	}
}

// The dispatcher returns from a recovery node before any debug check, and an
// approval gate returns before pause-after: neither pauses there.
func TestDebugRecoveryNodesAndGatesSkipTheirPauses(t *testing.T) {
	fastDebugPolls(t)
	fastApprovalPoll(t)
	ds := newDebugStore(t)
	fx := &fakeExec{bodies: map[string]Executor{
		"deploy": func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("broke")}
		},
		"rollback": func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("still broke")}
		},
	}}
	doc := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{
		{ID: "gate", Approval: &nodeproto.PlanApproval{Message: "go?"}},
		{ID: "deploy", Deps: deps("gate"), Modifiers: &nodeproto.PlanModifiers{OnFailure: "rollback"}},
		{ID: "rollback", OnFailureOf: "deploy"},
	}}
	dbg := Debug{PauseBefore: []string{"gate", "rollback"}, PauseAfter: []string{"gate", "rollback"}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan RunResult, 1)
	go func() { done <- hostDebug(t, ctx, ds, doc, dbg, fx.run) }()

	if got := <-ds.paused; got != "gate:"+store.PauseReasonBefore {
		t.Fatalf("paused %q, want the gate before it waits", got)
	}
	if _, err := ds.GetApproval(ctx, testRunID, "gate"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("approval row exists while the gate is paused: %v", err)
	}
	if err := ds.ReleaseDebugPause(ctx, testRunID, "gate", "alice", store.PauseReleaseManual); err != nil {
		t.Fatal(err)
	}
	<-ds.asked
	if err := resolveAs(store.ApprovalResolutionApproved, "alice", "")(ds.Store); err != nil {
		t.Fatal(err)
	}
	<-done

	wantRow(t, ds.Store, "rollback", sparkwing.Failed, "still broke")
	pauses, err := ds.ListDebugPauses(ctx, testRunID)
	if err != nil || len(pauses) != 1 {
		t.Errorf("pauses %+v err %v, want only the gate's pause-before", pauses, err)
	}
}

func TestDebugBounceRestartsTheNode(t *testing.T) {
	fastDebugPolls(t)
	ds := newDebugStore(t)
	started := make(chan struct{}, 2)
	var attempts []int
	var mu sync.Mutex
	fx := &fakeExec{bodies: map[string]Executor{"build": func(ctx context.Context, n *NodeRun) Result {
		mu.Lock()
		attempts = append(attempts, max(n.Attempt, 1))
		first := len(attempts) == 1
		mu.Unlock()
		started <- struct{}{}
		if !first {
			return Result{Outcome: sparkwing.Success, Output: []byte(`{"ok":true}`)}
		}
		<-ctx.Done()
		return Result{Outcome: sparkwing.Failed, Err: context.Cause(ctx)}
	}}}
	doc := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{{ID: "build"}}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan RunResult, 1)
	go func() { done <- hostDebug(t, ctx, ds, doc, Debug{Bounces: ds.Store}, fx.run) }()

	<-started
	if _, err := ds.RequestNodeBounce(ctx, testRunID, "build", "alice"); err != nil {
		t.Fatal(err)
	}
	res := <-done

	if res.Status != "success" {
		t.Fatalf("run %q: %v, want success after the bounce", res.Status, res.Err)
	}
	wantRow(t, ds.Store, "build", sparkwing.Success, "")
	if !slices.Equal(attempts, []int{1, 1}) {
		t.Errorf("attempts %v, want the node restarted as attempt 1", attempts)
	}
	bounces, err := ds.ListNodeBounces(ctx, testRunID)
	if err != nil || len(bounces) != 1 || bounces[0].Outcome != store.BounceBounced || bounces[0].ConsumedAt == nil {
		t.Errorf("bounces %+v err %v, want one consumed as bounced", bounces, err)
	}
	if got := ds.eventList(); !slices.Equal(got, []string{"build:node_bounced"}) {
		t.Errorf("events %v, want build:node_bounced", got)
	}
}

func TestDebugBounceAsTheNodeEndsIsMissed(t *testing.T) {
	fastDebugPolls(t)
	ds := newDebugStore(t)
	fx := &fakeExec{bodies: map[string]Executor{"build": func(ctx context.Context, _ *NodeRun) Result {
		if _, err := ds.RequestNodeBounce(ctx, testRunID, "build", "alice"); err != nil {
			return Result{Outcome: sparkwing.Failed, Err: err}
		}
		return Result{Outcome: sparkwing.Success}
	}}}
	doc := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{{ID: "build"}}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res := hostDebug(t, ctx, ds, doc, Debug{Bounces: ds.Store}, fx.run)

	if res.Status != "success" {
		t.Fatalf("run %q: %v", res.Status, res.Err)
	}
	bounces, err := ds.ListNodeBounces(ctx, testRunID)
	if err != nil || len(bounces) != 1 || bounces[0].Outcome != store.BounceMissed {
		t.Errorf("bounces %+v err %v, want one consumed as missed", bounces, err)
	}
	if got := ds.eventList(); !slices.Equal(got, []string{"build:node_bounce_missed"}) {
		t.Errorf("events %v, want build:node_bounce_missed", got)
	}
}

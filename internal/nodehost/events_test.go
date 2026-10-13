package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	fsstore "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the controller serves the same store, so every check reads what each backend stored.
func eventBackends(t *testing.T, body func(t *testing.T, st *store.Store, state storage.StateStore)) {
	t.Run("local", func(t *testing.T) {
		st := openStore(t)
		body(t, st, st)
	})
	t.Run("controller", func(t *testing.T) {
		st := openStore(t)
		srv := controller.New(st, slog.New(slog.DiscardHandler))
		hs := httptest.NewServer(srv.Handler())
		t.Cleanup(func() {
			hs.Close()
			_ = srv.Shutdown(context.Background())
		})
		body(t, st, client.New(hs.URL, nil))
	})
}

func hostEvents(t *testing.T, ctx context.Context, state storage.StateStore, cfg Config, doc nodeproto.PlanDoc) RunResult {
	t.Helper()
	if err := state.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: testRunID, Doc: doc, State: state, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), leaseToken: "tok"}
	res := RunResult{RunID: testRunID, Nodes: map[string]Result{}}
	runPlanned(ctx, &cfg, run, &res)()
	return res
}

func storedEvents(t *testing.T, st *store.Store) []store.Event {
	t.Helper()
	events, err := st.ListEventsAfter(t.Context(), testRunID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// safety: the store keeps a text payload as a JSON string, so a wanted payload is written as stored.
func wantEvent(t *testing.T, events []store.Event, node, kind, payload string) {
	t.Helper()
	if !slices.ContainsFunc(events, func(e store.Event) bool {
		return e.NodeID == node && e.Kind == kind && string(e.Payload) == payload
	}) {
		t.Errorf("no %s event on node %q with payload %q in %s", kind, node, payload, eventKinds(events))
	}
}

func countEvents(events []store.Event, node, kind string) int {
	n := 0
	for _, e := range events {
		if e.NodeID == node && e.Kind == kind {
			n++
		}
	}
	return n
}

func eventKinds(events []store.Event) string {
	var out []string
	for _, e := range events {
		out = append(out, e.NodeID+"/"+e.Kind+"="+string(e.Payload))
	}
	return strings.Join(out, ", ")
}

type eventLog struct {
	mu      sync.Mutex
	records map[string][]sparkwing.LogRecord
}

type eventNodeLog struct {
	l    *eventLog
	node string
}

func (n eventNodeLog) Emit(rec sparkwing.LogRecord) {
	n.l.mu.Lock()
	defer n.l.mu.Unlock()
	n.l.records[n.node] = append(n.l.records[n.node], rec)
}

func (eventNodeLog) Close() error { return nil }

func (l *eventLog) open(_ context.Context, _, node string) (NodeLog, error) {
	return eventNodeLog{l: l, node: node}, nil
}

type askingState struct {
	storage.StateStore
	asked chan string
}

func (a askingState) CreateApproval(ctx context.Context, appr store.Approval) error {
	err := a.StateStore.CreateApproval(ctx, appr)
	if err == nil {
		a.asked <- appr.NodeID
	}
	return err
}

func TestHostedRunEmitsTheDispatchersEvents(t *testing.T) {
	fastApprovalPoll(t)
	eventBackends(t, func(t *testing.T, st *store.Store, state storage.StateStore) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		art, err := fsstore.NewArtifactStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		workspace := t.TempDir()
		logs := &eventLog{records: map[string][]sparkwing.LogRecord{}}
		retry := func(auto bool) *nodeproto.PlanModifiers {
			return &nodeproto.PlanModifiers{Retry: 1, RetryAuto: auto}
		}
		doc := nodeproto.PlanDoc{Pipeline: "p", Nodes: []nodeproto.PlanNode{
			{ID: "ok"},
			{ID: "boom"},
			{ID: "after-boom", Deps: deps("boom")},
			{ID: "skipper"},
			{ID: "concurrent"},
			{ID: "superseded"},
			{ID: "flaky", Modifiers: retry(false)},
			{ID: "auto", Modifiers: retry(true)},
			{ID: "produce", Outputs: []string{"out.txt"}},
			{ID: "consume", Deps: deps("produce"), Consumes: []nodeproto.PlanConsume{{Producer: "produce", Into: "in"}}},
			{ID: "gate", Approval: &nodeproto.PlanApproval{Message: "ship it?"}},
			{ID: "targets", Closures: &nodeproto.PlanClosures{Generator: []string{testGenerator}}},
		}}
		var mu sync.Mutex
		calls := map[string]int{}
		exec := func(_ context.Context, n *NodeRun) Result {
			mu.Lock()
			calls[n.Plan.ID]++
			call := calls[n.Plan.ID]
			mu.Unlock()
			switch n.Plan.ID {
			case "boom":
				return Result{Outcome: sparkwing.Failed, Err: errors.New("boom")}
			case "skipper":
				return Result{Outcome: sparkwing.Skipped, Err: errors.New("skip reason")}
			case "concurrent":
				return Result{Outcome: sparkwing.SkippedConcurrent}
			case "superseded":
				return Result{Outcome: sparkwing.Superseded, Err: errors.New("lost the slot")}
			case "flaky", "auto":
				if call == 1 {
					return Result{Outcome: sparkwing.Failed, Err: errors.New("first try")}
				}
			case "produce":
				return writeFile(workspace, "out.txt", "artifact")
			case "targets":
				return Result{Outcome: sparkwing.Success, expansions: []expansion{{closure: testGenerator, nodes: []nodeproto.PlanNode{
					{ID: "child-0", Deps: deps("targets")}, {ID: "child-1", Deps: deps("targets")},
				}}}}
			}
			return Result{Outcome: sparkwing.Success}
		}
		asking := askingState{StateStore: state, asked: make(chan string, 1)}
		go func() {
			if recv(t, ctx, asking.asked) == "" {
				return
			}
			if _, err := st.ResolveApproval(ctx, testRunID, "gate", store.ApprovalResolutionApproved, "korey", ""); err != nil {
				t.Errorf("resolve: %v", err)
			}
		}()
		cfg := Config{
			Execute: exec, Artifacts: art, Session: SessionConfig{Dir: workspace},
			Attempts: &Attempts{OpenLog: logs.open, Masker: secrets.NewMasker()},
		}
		res := hostEvents(t, ctx, asking, cfg, doc)
		if res.Nodes["gate"].Outcome != sparkwing.Success || res.Nodes["consume"].Outcome != sparkwing.Success {
			t.Fatalf("gate %+v consume %+v, want both to succeed", res.Nodes["gate"], res.Nodes["consume"])
		}

		events := storedEvents(t, st)
		for _, id := range []string{"ok", "boom", "skipper", "concurrent", "superseded", "flaky", "produce", "consume", "gate", "targets", "child-0"} {
			if got := countEvents(events, id, "node_started"); got != 1 {
				t.Errorf("%s: %d node_started events, want 1", id, got)
			}
		}
		if got := countEvents(events, "auto", "node_started"); got != 2 {
			t.Errorf("auto: %d node_started events, want one per retry_auto dispatch", got)
		}
		if got := countEvents(events, "after-boom", "node_started"); got != 0 {
			t.Errorf("after-boom: %d node_started events, want none for a node that never ran", got)
		}
		wantEvent(t, events, "ok", "node_succeeded", "")
		wantEvent(t, events, "boom", "node_failed", `"boom"`)
		wantEvent(t, events, "after-boom", "node_cancelled", `"upstream-failed"`)
		wantEvent(t, events, "skipper", "node_skipped", `"skip reason"`)
		wantEvent(t, events, "concurrent", "node_skipped_concurrent", "")
		wantEvent(t, events, "superseded", "node_superseded", `"lost the slot"`)
		wantEvent(t, events, "flaky", "attempt_retry", `"attempt 2/2"`)
		wantEvent(t, events, "auto", "node_auto_retry", `"dispatch 2/2"`)
		wantEvent(t, events, "produce", "artifacts_published", `{"manifest_digest":"`+nodeRow(t, st, "produce").ArtifactManifest+`"}`)
		wantEvent(t, events, "consume", "artifacts_staged", `{"files":1}`)
		wantEvent(t, events, "gate", "approval_requested", `{"message":"ship it?","timeout_ms":0}`)
		wantEvent(t, events, "targets", "expansion_generated", `"2 children"`)

		logs.mu.Lock()
		defer logs.mu.Unlock()
		if !slices.ContainsFunc(logs.records["flaky"], func(r sparkwing.LogRecord) bool {
			return r.Event == "retry" && r.Msg == "retry attempt 2/2" && r.Attrs["attempt"] == 2 && r.Attrs["total"] == 2
		}) {
			t.Errorf("flaky log %+v, want the retry record", logs.records["flaky"])
		}
		if !slices.ContainsFunc(logs.records["auto"], func(r sparkwing.LogRecord) bool { return r.Msg == "auto-retry dispatch 2/2" }) {
			t.Errorf("auto log %+v, want the auto-retry line", logs.records["auto"])
		}
	})
}

func TestHostedRunEmitsConcurrencyEvents(t *testing.T) {
	eventBackends(t, func(t *testing.T, st *store.Store, state storage.StateStore) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		holdSlotOutside(t, st, "g:db")
		slots := storeSlots{st: st, queued: make(chan string, 1)}
		go func() {
			recv(t, ctx, slots.queued)
			if _, _, _, err := st.ReleaseAndNotify(ctx, "g:db", "other/x", "success", "", "", 0, store.DefaultConcurrencyLease); err != nil {
				t.Errorf("release outside hold: %v", err)
			}
		}()
		res := hostEvents(t, ctx, state, Config{
			Execute:   func(context.Context, *NodeRun) Result { return Result{Outcome: sparkwing.Success} },
			Admission: &Admission{Slots: slots},
		}, nodeproto.PlanDoc{Nodes: grouped("global", "queue", 1, 0, "b")})
		if res.Nodes["b"].Outcome != sparkwing.Success {
			t.Fatalf("b %+v, want success once promoted", res.Nodes["b"])
		}
		events := storedEvents(t, st)
		i := slices.IndexFunc(events, func(e store.Event) bool { return e.NodeID == "b" && e.Kind == "concurrency_wait" })
		if i < 0 {
			t.Fatalf("no concurrency_wait in %s", eventKinds(events))
		}
		var wait struct {
			Key, Kind string
			Holders   []struct {
				RunID string `json:"run_id"`
			}
		}
		if err := json.Unmarshal(events[i].Payload, &wait); err != nil || wait.Key != "g:db" || wait.Kind != "queued" || len(wait.Holders) != 1 || wait.Holders[0].RunID != "other" {
			t.Errorf("concurrency_wait payload %s (%v), want key g:db, kind queued, held by run other", events[i].Payload, err)
		}
		wantEvent(t, events, "b", "concurrency_promoted", "")
	})
}

func TestHostedRunEmitsPlanConcurrencyRefusals(t *testing.T) {
	eventBackends(t, func(t *testing.T, st *store.Store, state storage.StateStore) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		holdSlotOutside(t, st, "g:deploys")
		res := hostEvents(t, ctx, state, Config{
			Execute:   func(context.Context, *NodeRun) Result { return Result{Outcome: sparkwing.Success} },
			Admission: &Admission{Slots: storeSlots{st: st}},
		}, planGrouped("g:deploys", "skip", "a"))
		if res.Status != "success" {
			t.Fatalf("run %s %v, want a skipped run to succeed", res.Status, res.Err)
		}
		events := storedEvents(t, st)
		wantEvent(t, events, "", "plan_skipped_concurrent", "")
		wantEvent(t, events, "a", "node_skipped_concurrent", "")
	})
}

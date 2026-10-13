package nodehost

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func hostWith(t *testing.T, ctx context.Context, st *store.Store, cfg Config, doc nodeproto.PlanDoc) RunResult {
	t.Helper()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	skips, err := onlySkips(doc, cfg.Only, cfg.StartAt, cfg.StopAt)
	if err != nil {
		t.Fatalf("only: %v", err)
	}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), onlySkips: skips}
	res := RunResult{RunID: testRunID, Nodes: map[string]Result{}}
	runPlanned(ctx, &cfg, run, &res)()
	return res
}

func TestOnlyRunsTheMatchedNodesAndWhatTheyNeed(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	fx := &fakeExec{}
	doc := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{
		{ID: "setup"},
		{ID: "build-linux", Deps: deps("setup")},
		{ID: "build-mac", Deps: deps("setup")},
		{ID: "deploy", Deps: deps("build-linux", "build-mac")},
		{ID: "notify", OnFailureOf: "build-linux"},
	}}

	res := hostWith(t, ctx, st, Config{Execute: fx.run, Only: "build-l*"}, doc)
	if res.Status != "success" {
		t.Fatalf("status=%s err=%v, want success", res.Status, res.Err)
	}
	for _, id := range []string{"setup", "build-linux"} {
		wantRow(t, st, id, sparkwing.Success, "")
	}
	for _, id := range []string{"build-mac", "deploy"} {
		wantRow(t, st, id, sparkwing.Skipped, `outside --only="build-l*"`)
		if fx.ran(id) {
			t.Errorf("%s ran outside --sw-only", id)
		}
	}
	wantRow(t, st, "notify", sparkwing.Skipped, "did not fail")
}

func TestOnlyRefusesWhatTheDispatcherRefused(t *testing.T) {
	doc := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{{ID: "build"}, {ID: "test"}}}
	for _, tc := range []struct{ only, start, want string }{
		{"deploy", "", `--only="deploy" matched no jobs (declared: [build test])`},
		{"[", "", `--only="["`},
		{"build", "compile", "--only is mutually exclusive with --start-at / --stop-at"},
	} {
		if _, err := onlySkips(doc, tc.only, tc.start, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("only=%q start=%q: err=%v, want %q", tc.only, tc.start, err, tc.want)
		}
	}
}

func TestWorkersCapsTheNodesRunningAtOnce(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var mu sync.Mutex
	running, peak := 0, 0
	two, inlineRan, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var reachedTwo sync.Once
	go func() {
		<-two
		<-inlineRan
		close(release)
	}()
	body := func(ctx context.Context, n *NodeRun) Result {
		if m := n.Plan.Modifiers; m != nil && m.Inline {
			close(inlineRan)
			return Result{Outcome: sparkwing.Success}
		}
		mu.Lock()
		running++
		peak = max(peak, running)
		if running == 2 {
			reachedTwo.Do(func() { close(two) })
		}
		mu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
		}
		mu.Lock()
		running--
		mu.Unlock()
		return Result{Outcome: sparkwing.Success}
	}
	doc := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{
		{ID: "a"},
		{ID: "b"},
		{ID: "c"},
		{ID: "d"},
		{ID: "e"},
		{ID: "inline", Modifiers: &nodeproto.PlanModifiers{Inline: true}},
	}}

	res := hostWith(t, ctx, st, Config{Execute: body, Workers: 2}, doc)
	if res.Status != "success" {
		t.Fatalf("status=%s err=%v, want success: an inline node waited behind full workers", res.Status, res.Err)
	}
	if peak != 2 {
		t.Errorf("peak running = %d, want the cap of 2", peak)
	}
}

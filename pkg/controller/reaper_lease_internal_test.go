package controller

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// Two controllers sharing a store while a deploy overlaps them sweep it one at
// a time: one fails the stale run and judges the waiting node from its own view
// of the runners, and the other leaves both alone, so the node's reason does
// not alternate between their views.
func TestReaperRunsInOneOfTwoOverlappingControllers(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-stale", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Hour).UnixNano()
	if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET last_heartbeat_at = ? WHERE id = 'run-stale'`, stale); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "run-wait", Pipeline: "deploy", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-wait", NodeID: "apply", Status: "pending", NeedsLabels: []string{"tool:terraform"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-wait", "apply"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET ready_at = ?, placement_hold_from = ?, last_heartbeat = ?`,
		time.Now().Add(-time.Minute).UnixNano(), time.Now().Add(-time.Minute).UnixNano(), time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	_, pi, err := st.CreateToken("agent:pi", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAgentLabels(ctx, pi.Prefix, []string{"tool:terraform"}); err != nil {
		t.Fatal(err)
	}

	seesPi, blind := New(st, nil), New(st, nil)
	seesPi.runnerPresence.record(presenceKey{tokenPrefix: pi.Prefix, name: "pi"}, []string{"tool:terraform"}, nil, match.Profile{}, nil, time.Now())
	const interval = 10 * time.Second
	servers := []*Server{seesPi, blind}
	ran := make([]bool, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ran[i] = srv.reapLeased(ctx, interval)
		}()
	}
	wg.Wait()
	if ran[0] == ran[1] {
		t.Fatalf("sweeps ran = %v, want exactly one of the two controllers to sweep", ran)
	}
	loser := servers[0]
	if ran[0] {
		loser = servers[1]
	}

	run, err := st.GetRun(ctx, "run-stale")
	if err != nil || run.Status != "failed" {
		t.Fatalf("stale run = %+v, %v; want it failed by the sweep that ran", run, err)
	}
	attention := func() string {
		t.Helper()
		run, err := st.GetRun(ctx, "run-wait")
		if err != nil {
			t.Fatal(err)
		}
		return run.NeedsAttention
	}
	judged := attention()
	if (judged == "") != ran[0] {
		t.Fatalf("waiting node says %q after sweeps %v, want the view of the controller that swept", judged, ran)
	}
	if loser.reapLeased(ctx, interval) {
		t.Fatal("the other controller swept within the lease window")
	}
	if got := attention(); got != judged {
		t.Fatalf("waiting node says %q, then %q; the reason moved without a sweep", judged, got)
	}
}

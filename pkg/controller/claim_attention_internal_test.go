package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runAttention(t *testing.T, base, runID string) (single, listed string) {
	t.Helper()
	get := func(path string, out any) {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	var run struct {
		NeedsAttention string `json:"needs_attention"`
	}
	get("/api/v1/runs/"+runID, &run)
	var list struct {
		Runs []struct {
			ID             string `json:"id"`
			NeedsAttention string `json:"needs_attention"`
		} `json:"runs"`
	}
	get("/api/v1/runs", &list)
	for _, r := range list.Runs {
		if r.ID == runID {
			listed = r.NeedsAttention
		}
	}
	return run.NeedsAttention, listed
}

// A node no agent can claim marks its run with a reason naming what is
// missing, then the offline agent that could run it, and the mark clears once
// an agent is online and once one claims.
func TestClaimAttentionNamesWhyARunWaits(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	srv := New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "deploy", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "apply", Status: "pending", NeedsLabels: []string{"tool:terraform"}, RequestedCores: 4}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-1", "apply"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET ready_at = ?, placement_hold_from = ?`,
		time.Now().Add(-time.Minute).UnixNano(), time.Now().Add(-time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	expect := func(step, want string) {
		t.Helper()
		srv.sweepClaimAttention(ctx)
		if single, listed := runAttention(t, ts.URL, "run-1"); single != want || listed != want {
			t.Fatalf("%s: run says %q, run list says %q; want %q", step, single, listed, want)
		}
	}

	_, box, err := st.CreateToken("agent:box", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv.runnerPresence.record(presenceKey{tokenPrefix: box.Prefix, name: "box"}, []string{"tool:git"}, nil, match.Profile{}, nil, time.Now())
	expect("no agent has the tool", "node apply needs tool:terraform; no agent in team default has it")

	_, pi, err := st.CreateToken("agent:pi", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAgentLabels(ctx, pi.Prefix, []string{"tool:terraform"}); err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	if _, err := st.DB().ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE prefix = ?`, seen.UnixNano(), pi.Prefix); err != nil {
		t.Fatal(err)
	}
	expect("the eligible agent is offline", "node apply needs tool:terraform; eligible agent pi is offline (last seen 2026-09-28T10:00:00Z)")

	small := match.Profile{Capacity: match.Resources{Cores: 2}}
	srv.runnerPresence.record(presenceKey{tokenPrefix: pi.Prefix, name: "pi"}, []string{"tool:terraform"}, nil, small, nil, time.Now())
	expect("an online agent too small for the node", "node apply needs tool:terraform; every agent in team default that matches refuses it: pi (shape)")
	srv.runnerPresence.record(presenceKey{tokenPrefix: pi.Prefix, name: "pi"}, []string{"tool:terraform"}, nil, match.Profile{}, nil, time.Now())
	expect("the eligible agent is online", "")

	srv.runnerPresence.record(presenceKey{tokenPrefix: pi.Prefix, name: "pi"}, []string{"tool:terraform"}, nil, match.Profile{}, nil, time.Now().Add(-time.Hour))
	expect("the eligible agent went offline", "node apply needs tool:terraform; eligible agent pi is offline (last seen 2026-09-28T10:00:00Z)")
	if _, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "agent:pi", TokenPrefix: pi.Prefix}, "pi:1", time.Minute, []string{"tool:terraform"}); err != nil {
		t.Fatal(err)
	}
	if single, listed := runAttention(t, ts.URL, "run-1"); single != "" || listed != "" {
		t.Fatalf("after the claim the run says %q and the list %q; want neither", single, listed)
	}
}

// Sparkwing Cloud counts as an agent with its image's tools.
func TestClaimAttentionCountsSparkwingCloud(t *testing.T) {
	terraform := store.WaitingNode{NodeID: "apply", Team: "acme", Selector: []string{"tool:terraform"}}
	if got, want := claimAttention(terraform, nil, true),
		"node apply needs tool:terraform; no agent in team acme has it and Sparkwing Cloud runners don't provide it"; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
	build := store.WaitingNode{NodeID: "build", Team: "acme", Selector: []string{"tool:go"}}
	if got := claimAttention(build, nil, true); got != "" {
		t.Fatalf("a node Cloud can run is flagged: %q", got)
	}
	both := store.WaitingNode{NodeID: "ship", Team: "acme", Selector: []string{"tool:go", "tool:helm"}}
	if got, want := claimAttention(both, nil, true),
		"node ship needs tool:helm; no agent in team acme has it and Sparkwing Cloud runners don't provide it"; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

// An enrolled executor counts as online by its heartbeat, since its assisted
// offers record no presence, and is judged on its enrolled profile.
func TestClaimAttentionReadsAnEnrolledExecutorsHeartbeat(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	srv := New(st, nil)
	_, desk, err := st.CreateToken("agent:desk", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnrollExecutor(ctx, desk.Prefix, store.Executor{
		Name: "desk", Kind: "agent", Location: "local", Principal: "agent:desk",
		Capabilities: []string{"tool:terraform"}, MaxConcurrent: 1,
		Budget: store.ExecutorResource{Cores: 8, MemoryBytes: 16 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	node := store.WaitingNode{NodeID: "apply", Team: store.DefaultTeam, Selector: []string{"tool:terraform"}}
	heartbeat := func(at time.Time) string {
		t.Helper()
		if _, err := st.DB().ExecContext(ctx, `UPDATE executors SET last_seen = ?`, at.UnixNano()); err != nil {
			t.Fatal(err)
		}
		registered, err := st.ListRegisteredAgents(ctx, store.DefaultTeam)
		if err != nil {
			t.Fatal(err)
		}
		return claimAttention(node, sightAgents(registered, srv.runnerPresence.byToken(time.Now(), agentOnlineWithin), time.Now()), false)
	}
	if got := heartbeat(time.Now()); got != "" {
		t.Fatalf("a heartbeating enrolled executor with the tool leaves %q", got)
	}
	seen := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	if got, want := heartbeat(seen), "node apply needs tool:terraform; eligible agent desk is offline (last seen 2026-09-28T10:00:00Z)"; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
	node.Request = match.Resources{Cores: 16}
	if got := heartbeat(time.Now()); got != "node apply needs tool:terraform; every agent in team default that matches refuses it: desk (shape)" {
		t.Fatalf("a node larger than the enrolled budget = %q", got)
	}
}

// A new platform with the same labels is still worth storing.
func TestPresenceReportsAPlatformChange(t *testing.T) {
	reg := newRunnerPresenceRegistry()
	key := presenceKey{tokenPrefix: "swr_pi", name: "pi"}
	amd := match.Profile{OS: "linux", Arch: "amd64"}
	if !reg.record(key, []string{"tool:git"}, nil, amd, nil, time.Now()) {
		t.Fatal("a first record is not reported")
	}
	if reg.record(key, []string{"tool:git"}, nil, amd, nil, time.Now()) {
		t.Fatal("an unchanged record is reported")
	}
	if !reg.record(key, []string{"tool:git"}, nil, match.Profile{OS: "linux", Arch: "arm64"}, nil, time.Now()) {
		t.Fatal("a changed architecture is not reported")
	}
}

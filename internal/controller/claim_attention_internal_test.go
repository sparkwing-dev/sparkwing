package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestClaimAttentionNamesWhyARunWaits(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	st := e.Store
	now := time.Now().UTC()
	engineSQL(t, st, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','apply','pending',?,?,?)`, now.Add(-time.Minute).UnixNano(), []byte(`{"id":"apply"}`), []byte(`{"requires":{"value":["tool:terraform"]},"resources":{"value":{"cores":{"set":true,"value":4}}}}`))
	capacity := 8.0
	st.SetClaimProfileResolver(engineEnrollmentFunc(func(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
		return match.Profile{Capacity: match.Resources{Cores: capacity}}, match.Resources{Cores: capacity}, nil
	}))
	engineHTTPRoot(t, e, "a", "box", []capability.Grant{capability.NodesClaim})
	if err := st.RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: "box", Labels: []string{"tool:git"}}); err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil)
	expect := func(want string) {
		t.Helper()
		srv.sweepClaimAttention(t.Context(), now, claimAttentionBatch)
		var got string
		if err := teststore.SQL(t, st).QueryRow(`SELECT attention_reason FROM nodes WHERE team='a' AND run_id='run' AND node_id='apply'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("attention = %q, want %q", got, want)
		}
	}
	expect("node apply needs tool:terraform; no agent in team a has it")
	engineHTTPRoot(t, e, "a", "pi", []capability.Grant{capability.NodesClaim})
	if err := st.RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: "pi", Labels: []string{"tool:terraform"}}); err != nil {
		t.Fatal(err)
	}
	seen := now.Add(-time.Hour)
	engineSQL(t, st, `UPDATE capabilities SET last_used_at=? WHERE prefix='pi'`, seen.UnixNano())
	offline := "node apply needs tool:terraform; eligible agent pi is offline (last seen " + seen.Format(time.RFC3339) + ")"
	expect(offline)
	capacity = 2
	engineSQL(t, st, `UPDATE capabilities SET last_used_at=? WHERE prefix='pi'`, now.UnixNano())
	expect("node apply needs tool:terraform; every agent in team a that matches refuses it: pi (shape)")
	capacity = 8
	expect("")
	engineSQL(t, st, `UPDATE capabilities SET last_used_at=? WHERE prefix='pi'`, seen.UnixNano())
	expect(offline)
}

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

func TestClaimAttentionCommittedLivenessProperty(t *testing.T) {
	now := time.Unix(1800000000, 0)
	if err := quick.Check(func(delta int64) bool {
		delta = delta % int64(4*time.Minute)
		registered := []store.RegisteredAgent{{Name: "runner", LastSeen: now.Add(time.Duration(delta))}}
		got := sightAgents(registered, now)
		return len(got) == 1 && got[0].online == (delta <= 0 && delta >= -int64(agentOnlineWithin))
	}, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
	if sightAgents([]store.RegisteredAgent{{Name: "never seen"}}, now)[0].online {
		t.Fatal("unobserved runner is online")
	}
}

func TestClaimAttentionPassWrapsDespiteNewNodes(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	st := e.Store
	srv := New(st, nil)
	start := time.Now().UTC()
	seed := func(id string, ready time.Time) {
		t.Helper()
		engineSQL(t, st, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',?,?,?)`, id, ready.UnixNano(), []byte(`{"id":"`+id+`"}`), []byte(`{}`))
	}
	for _, id := range []string{"a", "b", "c"} {
		seed(id, start.Add(-time.Minute))
	}
	srv.sweepClaimAttention(t.Context(), start, 2)
	if err := st.AsOperator().SetNodeAttention(t.Context(), []store.NodeAttention{{Team: "a", RunID: "run", NodeID: "a"}}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		tick := start.Add(time.Duration(i) * 100 * time.Second)
		seed(fmt.Sprintf("d%dx", i), tick.Add(-time.Minute))
		seed(fmt.Sprintf("d%dy", i), tick.Add(-time.Minute))
		srv.sweepClaimAttention(t.Context(), tick, 2)
	}
	var reason string
	if err := teststore.SQL(t, st).QueryRow(`SELECT attention_reason FROM nodes WHERE team='a' AND run_id='run' AND node_id='a'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "needs an agent") {
		t.Fatalf("node behind cursor never reconsidered: %q", reason)
	}
}

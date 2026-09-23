package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestClassifyExecutionLocation(t *testing.T) {
	tests := []struct {
		name, principal, holder, runner string
		metered                         bool
		wantKind, wantName              string
	}{
		{"github", "github:42:koreyGambill/moonborn-ws", "runner:job-1", "actions-runner", false, "github-actions", "koreyGambill/moonborn-ws"},
		{"cloud", "agent:cloud", "pod:cloud-1", "cloud-1", true, "cloud", "cloud-1"},
		{"cluster", "agent:cluster", "k8s-job:sw-abc", "warm-pool", false, "cluster", "warm-pool"},
		{"machine", "agent:moonborn", "runner:moonborn:1", "moonborn", false, "machine", "moonborn"},
		{"job holder", "agent:cluster", "k8s-job:sw-abc", "", false, "cluster", "sw-abc"},
		{"agent holder", "", "agent:moonborn:1", "", false, "machine", "moonborn"},
		{"runner holder", "", "runner:moonborn:1", "", false, "machine", "moonborn"},
		{"pod holder", "", "pod:pool-a", "", false, "cluster", "pool-a"},
		{"unknown", "", "", "", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, name := classifyExecutionLocation(tt.principal, tt.holder, tt.runner, tt.metered)
			if kind != tt.wantKind || name != tt.wantName {
				t.Fatalf("location = %q %q, want %q %q", kind, name, tt.wantKind, tt.wantName)
			}
		})
	}
}

func TestHistoricalTriggerAttemptUsesMatchingClaim(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "old-trigger", Pipeline: "p", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: "agent:moonborn", TokenPrefix: "runner-token"}
	trigger, err := st.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fenced := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{Claimant: identity, ClaimGeneration: trigger.ClaimSeq})
	if err := st.CreateRun(fenced, store.Run{ID: trigger.ID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishTrigger(fenced, trigger.ID); err != nil {
		t.Fatal(err)
	}
	node := &store.Node{RunID: trigger.ID, NodeID: "inline", ExecutionAttempts: []store.ExecutionAttempt{
		{RunID: trigger.ID, NodeID: "inline", Attempt: 1, ClaimGeneration: trigger.ClaimSeq, HolderID: "trigger:old-coordinator"},
		{RunID: trigger.ID, NodeID: "inline", Attempt: 2, ClaimGeneration: trigger.ClaimSeq - 1, HolderID: "trigger:old-coordinator"},
		{RunID: trigger.ID, NodeID: "inline", Attempt: 3, ClaimGeneration: trigger.ClaimSeq, HolderID: "trigger:old-coordinator", ExecutorLocation: "cloud"},
	}}
	got, err := New(st, nil).publicNodesWithExecutionLocation(ctx, []*store.Node{node})
	if err != nil {
		t.Fatal(err)
	}
	a := got[0].ExecutionAttempts
	if a[0].ExecutionSite != "machine" || a[0].ExecutionSiteName != "moonborn" || a[1].ExecutionSite != "" || a[2].ExecutionSite != "cloud" || a[2].ExecutionSiteName != "moonborn" {
		t.Fatalf("historical trigger sites = %+v", a)
	}
}

func TestHistoricalAttemptsUseTheirOwnHolders(t *testing.T) {
	node := &store.Node{RunID: "old-run", NodeID: "build", ExecutionAttempts: []store.ExecutionAttempt{
		{RunID: "old-run", NodeID: "build", Attempt: 1, HolderID: "agent:moonborn:1"},
		{RunID: "old-run", NodeID: "build", Attempt: 2, HolderID: "k8s-job:job-17"},
		{RunID: "old-run", NodeID: "build", Attempt: 3, HolderID: "agent:cloud-helper:1", ExecutorLocation: "cloud"},
	}}
	got, err := (&Server{}).publicNodesWithExecutionLocation(context.Background(), []*store.Node{node})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].ExecutionAttempts) != 3 {
		t.Fatalf("public attempts = %+v", got)
	}
	a := got[0].ExecutionAttempts
	if a[0].ExecutionSite != "machine" || a[0].ExecutionSiteName != "moonborn" || a[1].ExecutionSite != "cluster" || a[1].ExecutionSiteName != "job-17" || a[2].ExecutionSite != "cloud" || a[2].ExecutionSiteName != "cloud-helper" {
		t.Fatalf("historical sites = %+v", a)
	}
}

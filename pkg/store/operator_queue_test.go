package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

func TestOperatorQueueCountsPartitionPersistedStates(t *testing.T) {
	s, _, now := maintenanceStore(t)
	for i, status := range []RunState{RunPending, RunRunning, RunSuccess, RunFailed, RunCancelled} {
		maintenanceRun(t, s, fmt.Sprintf("run-%d", i), i+1, status)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET team='b' WHERE id='run-1'`)
	want := map[string]int{}
	for _, state := range QueueStates() {
		want[state] = 0
	}
	random := rand.New(rand.NewPCG(401, 807))
	states := []NodeState{NodePending, NodeReady, NodeClaimed, NodeRunning, NodeApprovalPending, NodeSuccess, NodeFailed, NodeCancelled, NodeSkipped, NodeMemoized, NodeSuperseded}
	for i := range 256 {
		run := i % 5
		team := Team("a")
		if run == 1 {
			team = "b"
		}
		state := states[i%len(states)]
		row := NodeRow{Ref: NodeRef{fmt.Sprintf("run-%d", run), fmt.Sprintf("job-%d", i)}, Status: StatusPending}
		if random.IntN(2) == 0 || state == NodeReady {
			row.ReadyAt = now
		}
		switch state {
		case NodePending:
			row.ReadyAt = nil
		case NodeClaimed, NodeRunning:
			row.ClaimedBy, row.CapabilityPrefix, row.Generation = fmt.Sprintf("holder-%d", i%3), "claim", 1
			until := now.Add(time.Duration(random.IntN(3)-1) * time.Minute)
			row.LeaseExpiresAt = &until
			if state == NodeRunning {
				row.Status, row.Attempt = StatusRunning, 1
			}
		case NodeApprovalPending:
			row.Status = StatusApprovalPending
		case NodeSuccess, NodeFailed, NodeCancelled, NodeSkipped, NodeMemoized, NodeSuperseded:
			row.Status, row.Outcome, row.FinishedAt = StatusDone, state, now
		}
		if got, err := nodeState(row); err != nil || got != state {
			t.Fatalf("fixture: %s, %v", got, err)
		}
		var holder any
		if row.ClaimedBy != "" {
			holder = row.ClaimedBy
		}
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,outcome,ready_at,claimed_by,capability_prefix,generation,attempt,lease_expires_at,finished_at,spec_json,execution_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, team, row.Ref.RunID, row.Ref.NodeID, row.Status, row.Outcome, timeNS(row.ReadyAt), holder, row.CapabilityPrefix, row.Generation, row.Attempt, timeNS(row.LeaseExpiresAt), timeNS(row.FinishedAt), []byte(`{}`), []byte(`{}`))
		if run < 2 && row.Status != StatusDone {
			key := string(state)
			if state == NodePending {
				key = QueueStateWaiting
			}
			want[key]++
		}
	}
	for range 2 {
		got, err := s.AsOperator().CountNodesByQueueState(t.Context())
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("queue partition: %v, %v; want %v", got, err, want)
		}
		ready, err := s.AsOperator().CountPendingNodes(t.Context())
		if err != nil || ready != want[QueueStateReady] {
			t.Fatalf("ready: %d, %v; want %d", ready, err, want[QueueStateReady])
		}
		*now = now.Add(24 * time.Hour)
	}
}

func TestOperatorQueueCountsReturnEveryEmptyState(t *testing.T) {
	s, _, _ := maintenanceStore(t)
	got, err := s.AsOperator().CountNodesByQueueState(t.Context())
	if err != nil || len(got) != len(QueueStates()) {
		t.Fatalf("empty queue: %v, %v", got, err)
	}
	for _, state := range QueueStates() {
		if value, ok := got[state]; !ok || value != 0 {
			t.Fatalf("empty %s: %d, present=%t", state, value, ok)
		}
	}
}

func TestOperatorActiveRunnersUseStoreClockAndWindow(t *testing.T) {
	s, _, now := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, RunRunning)
	for i, offset := range []time.Duration{-time.Minute - time.Nanosecond, -time.Minute, 0, time.Minute, time.Minute} {
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,claimed_by,capability_prefix,generation,lease_expires_at,spec_json,execution_json) VALUES ('a','run',?,'pending',?,'claim',1,?,?,?)`, fmt.Sprintf("job-%d", i), fmt.Sprintf("runner-%d", min(i, 3)), now.Add(offset).UnixNano(), []byte(`{}`), []byte(`{}`))
	}
	count, err := s.AsOperator().CountActiveRunners(t.Context(), time.Minute)
	if err != nil || count != 3 {
		t.Fatalf("window: %d, %v; want 3", count, err)
	}
	*now = now.Add(time.Minute + time.Nanosecond)
	count, err = s.AsOperator().CountActiveRunners(t.Context(), time.Minute)
	if err != nil || count != 1 {
		t.Fatalf("advanced window: %d, %v; want 1", count, err)
	}
}

func TestTenantPendingTriggersIgnoreForeignQueueProperty(t *testing.T) {
	s, _, _ := maintenanceStore(t)
	want := map[Team]int{"a": 0, "b": 0}
	random := rand.New(rand.NewPCG(618, 211))
	for i := range 64 {
		id := fmt.Sprintf("run-%d", i)
		maintenanceRun(t, s, id, i+1, RunPending)
		team := Team("a")
		if random.IntN(2) == 0 {
			team = "b"
			rewriteSchemaExec(t, s.db, `UPDATE runs SET team=? WHERE id=?`, team, id)
		}
		status := []string{"pending", "claimed", "done"}[random.IntN(3)]
		rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,needs_labels,prefers_labels) VALUES (?,?,?,'trigger',1,1,?,?)`, team, id, status, []byte(`[]`), []byte(`[]`))
		if status == "pending" {
			want[team]++
		}
		for _, team := range []Team{"a", "b"} {
			tenant, err := s.ForTeam(t.Context(), team)
			if err != nil {
				t.Fatal(err)
			}
			got, err := tenant.CountPendingTriggers(t.Context())
			if err != nil || got != want[team] {
				t.Fatalf("%s backlog: %d, %v; want %d", team, got, err, want[team])
			}
		}
	}
}

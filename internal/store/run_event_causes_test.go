package store

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func seedEventSubscriptions(t *testing.T, s *Store) {
	t.Helper()
	for _, team := range []string{"a", "b"} {
		rewriteSchemaExec(t, s.db, `INSERT INTO run_event_subscriptions(team,id,url,event_types,statuses,pipelines,repositories,secret,created_principal,created_at,updated_at) VALUES (?,'sub_all','https://hooks.example.test/','["run.started","run.finished"]','[]','[]','[]','sealed','fixture',1,1)`, team)
	}
}

func queuedEvents(t *testing.T, s *Store, team string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT event_type,event_id,body FROM run_event_deliveries WHERE team=? ORDER BY event_id`, team)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var kind, id, body string
		if err := rows.Scan(&kind, &id, &body); err != nil {
			t.Fatal(err)
		}
		out = append(out, kind+" "+id+" "+body)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCancelDrainQueuesOneFinishedEventWithoutCallerProse(t *testing.T) {
	tenant, _, actor := stateFixture(t)
	seedEventSubscriptions(t, tenant.s)
	ctx := context.Background()
	request := runstate.RunTransition{RunID: "run", To: runstate.RunCancelled, Actor: actor, Cause: runstate.RunCancel, Reason: "stop it; token=canary-7731"}
	if _, err := tenant.TransitionRun(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionRun(ctx, request); err != nil {
		t.Fatal(err)
	}
	if got := queuedEvents(t, tenant.s, "a"); len(got) != 0 {
		t.Fatalf("cancel request queued %v", got)
	}
	if _, err := tenant.TransitionNode(ctx, nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: runstate.NodeCancelled, Actor: actor, Cause: runstate.CauseLeaseLapse}); err != nil {
		t.Fatal(err)
	}
	finish := runstate.RunTransition{RunID: "run", To: runstate.RunCancelled, Actor: actor, Cause: runstate.RunFinish}
	applied, err := tenant.TransitionRun(ctx, finish)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := tenant.TransitionRun(ctx, finish); err != nil || !replay.Replayed {
		t.Fatalf("replay %+v, %v", replay, err)
	}
	got := queuedEvents(t, tenant.s, "a")
	if len(got) != 1 || !strings.HasPrefix(got[0], "run.finished run/"+strconv.FormatInt(applied.StateSeq, 10)+" ") || !strings.Contains(got[0], `"status":"cancelled"`) {
		t.Fatalf("queued %v", got)
	}
	if strings.Contains(got[0], "canary-7731") || strings.Contains(got[0], "stop it") {
		t.Fatalf("caller prose reached the event: %s", got[0])
	}
	if other := queuedEvents(t, tenant.s, "b"); len(other) != 0 {
		t.Fatalf("other team's subscription matched: %v", other)
	}
}

func TestQueueTimeoutQueuesFinishedEventWithoutStart(t *testing.T) {
	s, tenant, _ := maintenanceStore(t)
	seedEventSubscriptions(t, s)
	maintenanceRun(t, s, "run", 1, runstate.RunPending)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json = ? WHERE team='a' AND id='run'`, []byte(`{"version":1,"admitted_at":"2027-01-01T00:00:00Z","request":{"options":{"claim_wait":{"set":true,"value":1}}},"provenance":{"trust":"asserted","pipeline":"p","writer":"account/fixture"}}`))
	actor := runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController}}
	if _, err := tenant.TransitionRun(context.Background(), runstate.RunTransition{RunID: "run", To: runstate.RunFailed, Actor: actor, Cause: runstate.RunQueueTimeout}); err != nil {
		t.Fatal(err)
	}
	got := queuedEvents(t, s, "a")
	if len(got) != 1 || !strings.HasPrefix(got[0], "run.finished run/1 ") || !strings.Contains(got[0], `"status":"failed"`) || !strings.Contains(got[0], `"started_at":null`) {
		t.Fatalf("queued %v", got)
	}
}

func TestOwnerLossQueuesFinishedEvent(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	seedEventSubscriptions(t, s)
	maintenanceRun(t, s, "run", 1, runstate.RunRunning)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json = ? WHERE team='a' AND id='run'`, []byte(`{"version":1,"request":{"origin":"local"},"provenance":{"trust":"asserted","pipeline":"pipeline","writer":"account/fixture"}}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,generation,lease_expires_at,needs_labels,prefers_labels,claimed_at,started_at,capability_prefix) VALUES ('a','run','claimed','trigger',1,1,'owner',1,?,?,?,1,1,'claim')`, now.Add(-time.Minute).UnixNano(), []byte(`[]`), []byte(`[]`))
	actor := runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController}}
	if _, err := tenant.TransitionRun(context.Background(), runstate.RunTransition{RunID: "run", To: runstate.RunFailed, Actor: actor, Cause: runstate.RunOwnerLost}); err != nil {
		t.Fatal(err)
	}
	got := queuedEvents(t, s, "a")
	if len(got) != 1 || !strings.HasPrefix(got[0], "run.finished run/1 ") || !strings.Contains(got[0], `"status":"failed"`) {
		t.Fatalf("queued %v", got)
	}
}

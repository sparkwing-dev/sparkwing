package controller

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestEngineRunDetailHidesPrivateInputs(t *testing.T) {
	property := func(value string, sealed, classified, declassified bool) bool {
		secret := "private-value-" + hex.EncodeToString([]byte(value))
		request := runspec.SealedRequest{Hash: runspec.Hash(secret + "-request"), Request: runspec.Request{
			Pipeline: "ci", RequestedArgs: map[string]string{"secret": secret, "public": "visible"},
			Env: runspec.EnvSnapshot{Digest: runspec.Hash(secret + "-env")},
		}}
		if classified {
			request.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"secret"}}
		}
		view := store.RunView{Snapshot: store.RunSnapshot{Run: store.RunRow{ID: "run", Request: request, PlanHash: secret + "-plan"}}}
		if sealed {
			view.Snapshot.Run.Spec = &runspec.Spec{Request: request, Hash: runspec.Hash(secret + "-spec"), Resolved: runspec.Resolved{Args: map[string]string{"secret": secret, "public": "visible"}}}
			if classified {
				view.Snapshot.Run.Spec.Resolved.SecretArgs = []string{"secret"}
				if declassified {
					view.Snapshot.Run.Spec.Resolved.SecretArgs = []string{}
				}
			}
		}
		got := engineRunDetail(view)
		raw, err := json.Marshal(got)
		if err != nil || bytes.Contains(raw, []byte(secret)) || got.Inputs.RequestedArgs["secret"] != runspec.RedactedArgValue {
			return false
		}
		if got.Inputs.RequestedArgs["public"] != map[bool]string{true: "visible", false: runspec.RedactedArgValue}[classified && !(sealed && declassified)] {
			return false
		}
		return request.Request.RequestedArgs["secret"] == secret && got.Jobs != nil && got.Attempts != nil && got.Logs != nil && got.Notices != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 256}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineRunDetailUsesCurrentInvocationLineage(t *testing.T) {
	request := runspec.SealedRequest{RunID: "original", Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: "earlier"}}}
	lineage := runspec.Lineage{RetryOf: "original", Retrier: runspec.Optional[runspec.Actor]{Set: true, Value: runspec.Actor{Kind: "account", ID: "retrier"}}}
	view := store.RunView{Snapshot: store.RunSnapshot{Run: store.RunRow{ID: "retry", Request: request, Lineage: lineage, Spec: &runspec.Spec{Request: request}}}}
	got := engineRunDetail(view)
	if got.ID != "retry" || !reflect.DeepEqual(got.Inputs.Lineage, lineage) || request.RunID != "original" || request.Request.Lineage.RetryOf != "earlier" {
		t.Fatalf("retry lineage: %+v", got)
	}
}

func TestEngineRunDetailReadsPersistedHistoryAndTaggedLogs(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitNode)
	now := time.Unix(1700000000, 0).UTC()
	identity := engine.Identity{Version: "v1.2.3"}
	spec, err := json.Marshal(runspec.Spec{Resolved: runspec.Resolved{Engine: identity, SecretArgs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.Store, `UPDATE runs SET status='failed',spec_json=?,spec_hash='spec',sealed_at=1,plan_json=?,plan_hash='plan',state_seq=7,started_at=?,finished_at=?,cancel_requested_at=? WHERE team='a' AND id='run'`, spec, []byte(`{"jobs":[{"id":"job"}]}`), now.UnixNano(), now.Add(time.Minute).UnixNano(), now.Add(time.Second).UnixNano())
	engineSQL(t, server.Store, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome,failure_reason) VALUES ('a','run','job',1,1,'runner','old-capability','runner','runner','remote',?,?,'failed','agent_lost'),('a','run','job',2,2,'runner','new-capability','runner','runner','remote',?,NULL,'','')`, now.UnixNano(), now.Add(time.Second).UnixNano(), now.Add(2*time.Second).UnixNano())
	engineSQL(t, server.Store, `INSERT INTO log_streams(team,run_id,id,subject_kind,node_id,attempt,generation,prefix,retention_class,created_at,updated_at) VALUES ('a','run','job-log','job_attempt','job',1,1,'private-object-prefix','r30',1,1)`)
	engineSQL(t, server.Store, `INSERT INTO log_streams(team,run_id,id,subject_kind,generation,unit_kind,source_node_id,source_generation,prefix,retention_class,created_at,updated_at) VALUES ('a','run','plan-log','owner_unit',3,'plan',NULL,NULL,'private-plan-prefix','r30',2,2),('a','run','eval-log','owner_unit',4,'eval','job',2,'private-eval-prefix','r30',3,3)`)
	got, err := server.RunDetail(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "run" || got.Team != "a" || got.Number != 1 || got.StateSeq != 7 || got.Engine == nil || *got.Engine != identity || got.StartedAt == nil || !got.StartedAt.Equal(now) || got.FinishedAt == nil || got.CancelRequestedAt == nil || len(got.Jobs) != 1 || len(got.Attempts) != 2 || len(got.Logs) != 3 {
		t.Fatalf("run detail: %+v", got)
	}
	if got.Attempts[0].Attempt != 1 || got.Attempts[0].Generation != 1 || got.Attempts[0].Outcome != "failed" || got.Attempts[0].Reason != "agent_lost" || got.Attempts[0].Executor != "runner" || got.Attempts[0].FinishedAt == nil || got.Attempts[1].Attempt != 2 || got.Attempts[1].FinishedAt != nil {
		t.Fatalf("attempt history: %+v", got.Attempts)
	}
	wantLogs := []blob.LogSubject{
		{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: 1}},
		{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 3}},
		{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: 4, Source: &blob.LogSource{NodeID: "job", Generation: 2}}},
	}
	if !reflect.DeepEqual(got.Logs, wantLogs) {
		t.Fatalf("tagged logs: %+v", got.Logs)
	}
	raw, err := json.Marshal(got)
	if err != nil || bytes.Contains(raw, []byte("private-")) || bytes.Contains(raw, []byte("capability")) {
		t.Fatal("status disclosed storage or capability internals")
	}
	owner.Target.Team = "b"
	if _, err := server.RunDetail(t.Context(), owner); err == nil {
		t.Fatal("read another team's detail")
	}
	owner.Target.Team, owner.Target.RunID = "a", "missing"
	if _, err := server.RunDetail(t.Context(), owner); err == nil {
		t.Fatal("read a missing run")
	}
}

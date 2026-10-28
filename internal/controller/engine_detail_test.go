package controller

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestEngineRunDetailHidesPrivateInputs(t *testing.T) {
	property := func(value string, sealed, classified, declassified bool) bool {
		secret := "private-value-" + hex.EncodeToString([]byte(value))
		request := runspec.SealedRequest{RunID: "run", Hash: runspec.Hash(secret + "-request"), Request: runspec.Request{
			Team: "a", Pipeline: "ci", RequestedArgs: map[string]string{"secret": secret, "public": "visible"},
			Env: runspec.EnvSnapshot{Digest: runspec.Hash(secret + "-env")},
		}}
		if classified {
			request.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"secret"}}
		}
		view := store.RunView{Snapshot: store.RunSnapshot{StoreNow: time.Unix(1, 0), Run: store.RunRow{Team: "a", Execution: runstate.Execution{ID: "run", Request: request, PlanHash: secret + "-plan"}}}}
		if sealed {
			view.Snapshot.Run.Spec = &runspec.Spec{Request: request, Hash: runspec.Hash(secret + "-spec"), Resolved: runspec.Resolved{Args: map[string]string{"secret": secret, "public": "visible"}}}
			if classified {
				view.Snapshot.Run.Spec.Resolved.SecretArgs = []string{"secret"}
				if declassified {
					view.Snapshot.Run.Spec.Resolved.SecretArgs = []string{}
				}
			}
		}
		got, projectionErr := runDetailResponse(capability.Target{Team: "a", RunID: view.Snapshot.Run.ID}, RunStatusFacts{Snapshot: view.Snapshot, Attempts: view.Attempts, Logs: view.Logs})
		raw, err := json.Marshal(got)
		if projectionErr != nil || err != nil || bytes.Contains(raw, []byte(secret)) || got.Inputs.RequestedArgs["secret"] != runspec.RedactedArgValue {
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
	request := runspec.SealedRequest{RunID: "original", Request: runspec.Request{Team: "a", Lineage: runspec.Lineage{RetryOf: "earlier"}}}
	lineage := runspec.Lineage{RetryOf: "original", Retrier: runspec.Optional[runspec.Actor]{Set: true, Value: runspec.Actor{Kind: "account", ID: "retrier"}}}
	retry := &runstate.RetrySelection{Mode: "failed", Results: map[string]runstate.MaterializedResultSource{"test": {}, "build": {}}}
	view := store.RunView{Snapshot: store.RunSnapshot{StoreNow: time.Unix(1, 0), Run: store.RunRow{Team: "a", Execution: runstate.Execution{ID: "retry", Request: request, Lineage: lineage, Retry: retry, Spec: &runspec.Spec{Request: request}}}}}
	got, err := runDetailResponse(capability.Target{Team: "a", RunID: "retry"}, RunStatusFacts{Snapshot: view.Snapshot})
	if err != nil || got.ID != "retry" || !reflect.DeepEqual(got.Inputs.Lineage, lineage) || request.RunID != "original" || request.Request.Lineage.RetryOf != "earlier" {
		t.Fatalf("retry lineage: %+v", got)
	}
	if !reflect.DeepEqual(got.Retry, &controllerwire.RetryDetail{Mode: "failed", Reused: []string{"build", "test"}}) {
		t.Fatalf("retry reuse: %+v", got.Retry)
	}
}

func TestEngineRunDetailCarriesJobDeclarations(t *testing.T) {
	request := runspec.SealedRequest{RunID: "run", Request: runspec.Request{Team: "a"}}
	node := func(id, spec string) runstate.NodeRow {
		return runstate.NodeRow{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(id)}, Spec: json.RawMessage(spec)}
	}
	view := store.RunView{Snapshot: store.RunSnapshot{StoreNow: time.Unix(1, 0), Run: store.RunRow{Team: "a", Execution: runstate.Execution{ID: "run", Request: request}}, Nodes: []runstate.NodeRow{
		node("build", `{"id":"build"}`),
		node("test", `{"id":"test","deps":["build"],"deps_if_present":["lint","build"]}`),
		node("deploy", `{"id":"deploy","deps":["test","build"],"groups":["release"],"approval":{},"on_failure_of":"test"}`),
		node("notify", `{"id":"notify","on_failure_of":"absent"}`),
	}}}
	got, err := runDetailResponse(capability.Target{Team: "a", RunID: "run"}, RunStatusFacts{Snapshot: view.Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	needs := map[string][]string{}
	for _, job := range got.Jobs {
		needs[job.ID] = job.Needs
	}
	if want := map[string][]string{"build": nil, "test": {"build"}, "deploy": {"test", "build"}, "notify": nil}; !reflect.DeepEqual(needs, want) {
		t.Fatalf("job needs: %v, want %v", needs, want)
	}
	deploy, notify := got.Jobs[2], got.Jobs[3]
	if !reflect.DeepEqual(deploy.Groups, []string{"release"}) || !deploy.Approval || deploy.OnFailureOf != "test" || notify.OnFailureOf != "" || notify.Approval {
		t.Fatalf("job declarations: %+v %+v", deploy, notify)
	}
	view.Snapshot.Nodes[2].Spec = json.RawMessage(`{"id":"deploy","deps":["test"],"env":{"TOKEN":"private-secret"}}`)
	view.Snapshot.Nodes[3].Spec = json.RawMessage(`{`)
	got, err = runDetailResponse(capability.Target{Team: "a", RunID: "run"}, RunStatusFacts{Snapshot: view.Snapshot})
	if err != nil || !reflect.DeepEqual(got.Jobs[2].Needs, []string{"test"}) || got.Jobs[3].ID != "notify" || bytes.Contains(must(json.Marshal(got)), []byte("private-secret")) {
		t.Fatalf("a declaration the projection does not read failed or leaked into run detail: %+v %v", got.Jobs, err)
	}
}

func TestEngineRunDetailBeforePlanningNeedsNoVersionPolicy(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
	got, err := server.RunDetail(t.Context(), owner)
	if err != nil || got.ID != "run" || got.Engine != nil || got.Notices == nil || len(got.Notices) != 0 {
		t.Fatalf("unplanned status: %+v err=%v", got, err)
	}
}

func TestEngineRunDetailReadsPersistedHistoryAndTaggedLogs(t *testing.T) {
	server, tenant := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitNode)
	now := time.Unix(1700000000, 0).UTC()
	identity := engineid.Identity{Version: "v1.2.3"}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(runspec.Spec{Request: run.Request, Resolved: runspec.Resolved{Engine: identity, SecretArgs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.Store, `UPDATE runs SET status='failed',spec_json=?,spec_hash='spec',sealed_at=1,plan_json=?,plan_hash='plan',state_seq=7,started_at=?,finished_at=?,cancel_requested_at=? WHERE team='a' AND id='run'`, spec, []byte(`{"jobs":[{"id":"job"}]}`), now.UnixNano(), now.Add(time.Minute).UnixNano(), now.Add(time.Second).UnixNano())
	engineSQL(t, server.Store, `UPDATE nodes SET generation=2,attempt=2 WHERE run_id='run' AND node_id='job'`)
	engineSQL(t, server.Store, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome,failure_reason) VALUES ('a','run','job',1,1,'runner','old-capability','runner','runner','remote',?,?,'failed','agent_lost'),('a','run','job',2,2,'runner','new-capability','runner','runner','remote',?,NULL,'','')`, now.UnixNano(), now.Add(time.Second).UnixNano(), now.Add(2*time.Second).UnixNano())
	engineSQL(t, server.Store, `INSERT INTO log_streams(team,run_id,id,subject_kind,node_id,attempt,generation,prefix,retention_class,created_at,updated_at) VALUES ('a','run','job-log','job_attempt','job',1,1,'private-object-prefix','r30',1,1)`)
	engineSQL(t, server.Store, `INSERT INTO log_streams(team,run_id,id,subject_kind,generation,unit_kind,source_node_id,source_generation,prefix,retention_class,created_at,updated_at) VALUES ('a','run','plan-log','owner_unit',3,'plan',NULL,NULL,'private-plan-prefix','r30',2,2),('a','run','eval-log','owner_unit',4,'eval','job',2,'private-eval-prefix','r30',3,3)`)
	policy := &engineVersionFake{notices: []engineid.Notice{{Kind: engineid.NoticeSupportEnding, Version: identity.Version, Reason: "signed warning"}}}
	server.VersionPolicy = engineVersionPolicy(policy)
	got, err := server.RunDetail(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "run" || got.Team != "a" || got.Number != 1 || got.StateSeq != 7 || got.Engine == nil || *got.Engine != identity || got.StartedAt == nil || !got.StartedAt.Equal(now) || got.FinishedAt == nil || got.CancelRequestedAt == nil || len(got.Jobs) != 1 || len(got.Attempts) != 2 || len(got.Logs) != 3 {
		t.Fatalf("run detail: %+v", got)
	}
	if got.Attempts[0].Attempt != 1 || got.Attempts[0].Generation != 1 || got.Attempts[0].Outcome != "failed" || got.Attempts[0].Reason != "agent_lost" || got.Attempts[0].Executor != "runner" || got.Attempts[0].ExecutorName != "runner" || got.Attempts[0].Location != "remote" || got.Attempts[0].FinishedAt == nil || got.Attempts[1].Attempt != 2 || got.Attempts[1].FinishedAt != nil {
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
	if !reflect.DeepEqual(got.Notices, policy.notices) || policy.evaluated != identity.Version {
		t.Fatalf("version notices: %+v version=%s", got.Notices, policy.evaluated)
	}
	raw, err := json.Marshal(got)
	if err != nil || bytes.Contains(raw, []byte("private-")) || bytes.Contains(raw, []byte("capability")) {
		t.Fatal("status disclosed storage or capability internals")
	}
	server.VersionPolicy = nil
	if got, err := server.RunDetail(t.Context(), owner); err == nil || !reflect.DeepEqual(got, controllerwire.RunDetail{}) {
		t.Fatalf("missing policy returned partial detail: %+v err=%v", got, err)
	}
	policy.indexErr = errors.New("policy unavailable")
	server.VersionPolicy = engineVersionPolicy(policy)
	if got, err := server.RunDetail(t.Context(), owner); err == nil || !reflect.DeepEqual(got, controllerwire.RunDetail{}) {
		t.Fatalf("failed policy returned partial detail: %+v err=%v", got, err)
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

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

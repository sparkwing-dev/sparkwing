package controller

import (
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestEngineReportedFailureReachesRecovery(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
	server.claimProfiles = engineProfileFake{}
	engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	engineAcceptedFixturePlan(t, server, "a", "run", "job", "recovery")
	engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','recovery','pending',?,?)`, []byte(`{"id":"recovery","on_failure_of":"job"}`), []byte(`{}`))
	claim, err := server.ReconcileJob(t.Context(), owner, controllerwire.ReconcileRequest{Policy: "none"})
	if err != nil || claim.Claim == nil {
		t.Fatalf("claim: %+v, %v", claim, err)
	}
	principal, err := server.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(claim.Claim.Capability)))
	if err != nil {
		t.Fatal(err)
	}
	executor := capability.Authorized{Principal: principal, Binding: claim.Claim.Binding, Target: owner.Target}
	engineAdmitExecution(t, server.Store, executor.Principal)
	if _, err := server.TransitionJob(t.Context(), executor, controllerwire.JobTransitionRequest{To: "running", Cause: "start", SourceAttempt: new(int64(0))}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"job":"other","stage":"step:verify","error":{"code":"sdk_error","message":"masked"}}`,
		`{"job":"job","stage":"","error":{"code":"sdk_error","message":"masked"}}`,
		`{"job":"job","stage":"step:verify","error":null}`,
		`{"job":"job","stage":"step:verify","error":{"code":"","message":"masked"}}`,
	} {
		var failure nodeproto.JobFailure
		if err := json.Unmarshal([]byte(raw), &failure); err != nil {
			continue
		}
		if _, err := server.TransitionJob(t.Context(), executor, controllerwire.JobTransitionRequest{To: "failed", Cause: "report", SourceAttempt: new(int64(1)), Result: &controllerwire.JobResult{Outcome: "failed", Failure: &failure}}); err == nil {
			t.Fatalf("invalid failure committed: %s", raw)
		}
	}
	var report controllerwire.JobTransitionRequest
	if err := json.Unmarshal([]byte(`{"to":"failed","cause":"report","result":{"outcome":"failed","reason":"step_failed","failure":{"job":"job","stage":"step:verify","error":{"code":"future_failure","message":"masked failure","hint":"retry verification","details":{"exit_code":7}}}}}`), &report); err != nil {
		t.Fatal(err)
	}
	report.SourceAttempt = new(int64(1))
	first, err := server.TransitionJob(t.Context(), executor, report)
	if err != nil || first.StateSeq != 3 {
		t.Fatalf("report: %+v, %v", first, err)
	}
	replayed, err := server.TransitionJob(t.Context(), executor, report)
	if err != nil || !replayed.Replayed || replayed.StateSeq != first.StateSeq {
		t.Fatalf("replay: %+v, %v", replayed, err)
	}
	owner.Target.SourceGeneration = claim.Claim.Binding.Generation
	failure, err := server.ReadFailure(t.Context(), owner)
	want := &nodeproto.JobFailure{Job: "job", Stage: "step:verify", Error: &apierr.Error{Code: "future_failure", Message: "masked failure", Hint: "retry verification", Details: map[string]any{"exit_code": float64(7)}}}
	if err != nil || !reflect.DeepEqual(failure, want) {
		t.Fatalf("recovery failure: %+v, %v", failure, err)
	}
}

func FuzzEngineReportedFailureProjection(f *testing.F) {
	f.Add("build", "step:verify", "sdk_error", "masked failure", "retry verification", "exit_code", "7")
	f.Add("generated/build", "approval", "future_failure", "", "", "nested", "value")
	f.Fuzz(func(t *testing.T, job, stage, code, message, hint, key, value string) {
		input := controllerwire.JobTransitionRequest{To: "failed", Cause: "report", Result: &controllerwire.JobResult{Outcome: "failed", Failure: &nodeproto.JobFailure{Job: job, Stage: stage, Error: &apierr.Error{Code: apierr.Code(code), Message: message, Hint: hint, Details: map[string]any{key: value}}}}}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var report controllerwire.JobTransitionRequest
		if err := json.Unmarshal(raw, &report); err != nil {
			return
		}
		got, err := captureEngineTransition(t.Context(), report)
		if err != nil {
			if got != nil {
				t.Fatal("refused failure reached persistence")
			}
			return
		}
		command, ok := got.(store.ReportNode)
		if !ok || command.Result == nil || !reflect.DeepEqual(command.Result.Failure, report.Result.Failure) || command.Result.MaskedError != report.Result.Failure.Error.Message {
			t.Fatalf("failure projection changed: %+v, %v", got, err)
		}
	})
}

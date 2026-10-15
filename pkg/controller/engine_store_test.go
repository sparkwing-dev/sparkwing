package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func engineOwnerAuthorization() capability.Authorized {
	binding := capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "host", Generation: 4}
	return capability.Authorized{
		Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, Capability: capability.Capability{Team: "a", Prefix: "owner", Unit: capability.UnitTrigger, Binding: binding}},
		Binding:   binding,
		Target:    capability.Target{Team: "a", RunID: "run", NodeID: "job"},
	}
}

func TestEngineIntakeKeepsSessionAuthorityAndRejectsTeamConfusion(t *testing.T) {
	authorized := engineOwnerAuthorization()
	authorized.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "session"}
	authorized.Principal.Capability.Prefix = ""
	body := controllerwire.CreateRunRequest{Request: runspec.Request{Pipeline: "ci", Team: "a"}, IdempotencyKey: "submission"}
	got, err := IntakeForStore(authorized, body)
	if err != nil || got.Authority != authorized.Principal.Authority || got.CapabilityPrefix != "" || got.IdempotencyKey != body.IdempotencyKey || !reflect.DeepEqual(got.Request, body.Request) {
		t.Fatalf("intake: %+v, %v", got, err)
	}
	for _, mutate := range []func(*capability.Authorized, *controllerwire.CreateRunRequest){
		func(a *capability.Authorized, _ *controllerwire.CreateRunRequest) { a.Target.Team = "b" },
		func(_ *capability.Authorized, b *controllerwire.CreateRunRequest) { b.Request.Team = "b" },
		func(a *capability.Authorized, _ *controllerwire.CreateRunRequest) {
			a.Principal.Authority = capability.AuthorityRef{}
		},
	} {
		a, b := authorized, body
		mutate(&a, &b)
		if _, err := IntakeForStore(a, b); err == nil {
			t.Fatal("accepted mismatched or absent authority")
		}
	}
}

func TestEnginePlanUsesPersistedRequestAndServerFence(t *testing.T) {
	authorized := engineOwnerAuthorization()
	request := runspec.SealedRequest{Version: 1, RunID: "run", Hash: "sealed-request"}
	body := controllerwire.PlanSubmission{Declarations: runspec.CheckoutDeclarations{Version: 1}, Plan: json.RawMessage(`{"jobs":[]}`), Resolved: runspec.Resolved{SecretArgs: []string{}}}
	run := store.RunRow{ID: "run", Request: request}
	got, err := planForStore(authorized, body, run)
	if err != nil || got.RunID != "run" || got.Actor.Kind != store.ActorRunHost || got.Actor.Authority != authorized.Principal.Authority || got.Actor.Owner != (store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: "run", Holder: "host", Generation: 4}) || !reflect.DeepEqual(got.Seal.Request, request) || !reflect.DeepEqual(got.Seal.Declarations, body.Declarations) || !reflect.DeepEqual(got.Seal.Resolved, body.Resolved) || !reflect.DeepEqual(got.Seal.Plan, body.Plan) || !got.Seal.Now.IsZero() {
		t.Fatalf("plan: %+v, %v", got, err)
	}
	run.ID = "other"
	if _, err := planForStore(authorized, body, run); err == nil {
		t.Fatal("used a different run's request seal")
	}
	run.ID, run.Request.RunID = "run", "original-run"
	if got, err := planForStore(authorized, body, run); err != nil || got.Seal.Request.RunID != "original-run" {
		t.Fatalf("retry request seal: %+v, %v", got, err)
	}
}

func TestEngineExpansionKeepsIndependentFencesAndStructuredFailure(t *testing.T) {
	authorized := engineOwnerAuthorization()
	authorized.Principal.Capability.Unit = capability.UnitEval
	authorized.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "source", Generation: 8, Holder: "evaluator", Expansion: &capability.LeaseRef{Kind: capability.UnitEval, ID: "eval-unit", Generation: 3, Holder: "evaluator"}}
	authorized.Target.NodeID = "source"
	body := controllerwire.ExpansionSubmission{SourceGeneration: 8, Failure: &apierr.Error{Code: apierr.PlanFailed, Message: "masked failure"}}
	decode := func(raw []byte) (*apierr.Error, error) {
		var failure apierr.Error
		err := json.Unmarshal(raw, &failure)
		return &failure, err
	}
	got, err := expansionForStore(authorized, body, decode)
	if err != nil || got.Source != (store.NodeRef{RunID: "run", NodeID: "source"}) || got.SourceGeneration != 8 || got.Actor.Owner.Generation != 3 || got.Actor.Owner.SourceGeneration != 8 || got.Failure == nil || got.Failure.Code != apierr.PlanFailed || got.Failure.MaskedError != body.Failure.Message {
		t.Fatalf("expansion: %+v, %v", got, err)
	}
	for _, mutate := range []func(*capability.Authorized, *controllerwire.ExpansionSubmission){
		func(_ *capability.Authorized, b *controllerwire.ExpansionSubmission) { b.SourceGeneration++ },
		func(_ *capability.Authorized, b *controllerwire.ExpansionSubmission) {
			b.Body = json.RawMessage(`{"jobs":[]}`)
		},
		func(_ *capability.Authorized, b *controllerwire.ExpansionSubmission) { b.Failure = nil },
		func(a *capability.Authorized, _ *controllerwire.ExpansionSubmission) { a.Binding.Expansion = nil },
		func(a *capability.Authorized, _ *controllerwire.ExpansionSubmission) { a.Target.NodeID = "other" },
	} {
		a, b := authorized, body
		mutate(&a, &b)
		if _, err := expansionForStore(a, b, decode); err == nil {
			t.Fatal("accepted invalid expansion authority or body")
		}
	}
	body.Failure, body.Body = nil, json.RawMessage(`{"jobs":[]}`)
	if got, err := ExpansionForStore(authorized, body); err != nil || string(got.Body) != string(body.Body) || got.Failure != nil {
		t.Fatalf("empty successful expansion: %+v, %v", got, err)
	}
	body.Failure, body.Body = &apierr.Error{Code: apierr.PlanFailed, Message: "masked failure"}, nil
	refusal := errors.New("invalid inner error")
	if _, err := expansionForStore(authorized, body, func([]byte) (*apierr.Error, error) { return nil, refusal }); !errors.Is(err, refusal) {
		t.Fatalf("decoder refusal: %v", err)
	}
}

func TestEngineReconcileDeliversClaimWithoutPublishingItsBearer(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	result := store.ReconciledNode{Kind: store.Execute, Node: store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Generation: 5}, Claim: &store.ClaimAward{Holder: "runner", Generation: 5, CapabilityPrefix: "claim", LeaseExpiresAt: now}, Bearer: []byte("one-time-bearer")}
	cap := capability.Capability{Unit: capability.UnitNode, UnitID: "awarded-unit", Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "runner", Generation: 5}, Billing: &capability.BillingProvenance{ExecutorClass: "runner"}}
	run := store.RunRow{ID: "run", Request: runspec.SealedRequest{Hash: "request"}, Spec: &runspec.Spec{Hash: "spec"}, Plan: json.RawMessage(`{"jobs":[]}`), PlanHash: "plan"}
	got := engineReconciledClaim(cap, result, run)
	if got.Capability != "one-time-bearer" || got.Binding != cap.Binding || got.Accepted == nil || got.Accepted.Spec.Hash != "spec" || got.Request.Hash != "request" || got.Runner.Name != "runner" || got.LeaseExpiresAt != now || got.Execution == nil {
		t.Fatalf("reconciled claim lost execution authority: %+v", got)
	}
	raw, err := json.Marshal(engineJob(result.Node))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, result.Bearer) {
		t.Fatal("public job view contains a capability")
	}
}

func TestEngineJobResultPreservesRecordsAndRetry(t *testing.T) {
	authorized := engineOwnerAuthorization()
	authorized.Principal.Capability.Unit = capability.UnitNode
	authorized.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "runner", Generation: 9}
	output, manifest := blob.RecordKey{Name: "output"}, blob.RecordKey{Name: "manifest"}
	exit := 7
	retry := time.Unix(100, 0).UTC()
	failure := &nodeproto.JobFailure{Job: "job", Stage: "step:verify", Error: &apierr.Error{Code: apierr.SDKError, Message: "masked"}}
	body := controllerwire.JobTransitionRequest{To: "running", Cause: "retry", RetryAt: &retry, Result: &controllerwire.JobResult{Outcome: "failed", Reason: "exit", Failure: failure, ExitCode: &exit, Output: &output, Manifest: &manifest}}
	got, err := nodeTransitionForStore(authorized, body)
	if err != nil || got.Ref != (store.NodeRef{RunID: "run", NodeID: "job"}) || got.Actor.Kind != store.ActorHolder || got.Actor.Holder != "runner" || got.Actor.Generation != 9 || got.To != store.NodeRunning || got.Cause != store.CauseRetry || got.Extra.RetryAt != &retry || !reflect.DeepEqual(got.Extra.Result, &store.NodeResult{Outcome: store.NodeFailed, Reason: "exit", MaskedError: "masked", ExitCode: &exit, OutputRecord: &output, ManifestRecord: &manifest, Failure: failure}) {
		t.Fatalf("job transition: %+v, %v", got, err)
	}
}

func TestEngineFinishBindsOwnerAndPublisher(t *testing.T) {
	authorized := engineOwnerAuthorization()
	body := controllerwire.FinishRequest{Outcome: "failed", Reason: "job_failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "masked"}}
	got, err := finishForStore(authorized, body)
	if err != nil || got.Transition.Cause != store.RunFinish || got.Transition.Actor.Kind != store.ActorRunHost || got.Unit != got.Transition.Actor.Owner || got.Unit.Generation != 4 || got.Transition.MaskedError != "masked" {
		t.Fatalf("owner finish: %+v, %v", got, err)
	}
	authorized.Principal.Capability.Unit = ""
	authorized.Principal.Capability.Data = &capability.DataScope{Use: capability.DataPublish, RunID: "run"}
	authorized.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
	got, err = finishForStore(authorized, body)
	if err != nil || got.Transition.Cause != store.RunPublish || got.Transition.Actor.Kind != store.ActorOperator || got.Unit != (store.OwnerLeaseRef{}) {
		t.Fatalf("publisher finish: %+v, %v", got, err)
	}
}

package controller

import (
	"bytes"
	"encoding/json"
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

func TestEngineReconcileDeliversClaimWithoutPublishingItsBearer(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	result := store.ReconciledNode{Kind: store.Execute, Node: store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Generation: 5}, Claim: &store.ClaimAward{Holder: "runner", Generation: 5, CapabilityPrefix: "claim", LeaseExpiresAt: now}, Bearer: []byte("one-time-bearer")}
	cap := capability.Capability{Unit: capability.UnitNode, UnitID: "awarded-unit", Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "runner", Generation: 5}, Billing: &capability.BillingProvenance{ExecutorClass: "runner"}}
	run := store.RunRow{ID: "run", Request: runspec.SealedRequest{Hash: "request"}, Spec: &runspec.Spec{Hash: "spec"}, Plan: json.RawMessage(`{"jobs":[]}`), PlanHash: "plan"}
	got := engineReconciledClaim(cap, result, run)
	if got.Capability != "one-time-bearer" || got.Binding != cap.Binding || got.Accepted == nil || got.Accepted.Spec.Hash != "spec" || got.Request.Hash != "request" || got.Runner.Name != "runner" || got.LeaseExpiresAt != now || got.Execution == nil {
		t.Fatalf("reconciled claim lost execution authority: %+v", got)
	}
	raw, err := json.Marshal(snapshotJob(result.Node))
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
	authorized.Access.Credential = capability.CredentialPublisher
	authorized.Principal.Capability.Data = &capability.DataScope{Use: capability.DataPublish, RunID: "run"}
	authorized.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
	got, err = finishForStore(authorized, body)
	if err != nil || got.Transition.Cause != store.RunPublish || got.Transition.Actor.Kind != store.ActorOperator || got.Unit != (store.OwnerLeaseRef{}) {
		t.Fatalf("publisher finish: %+v, %v", got, err)
	}
}

package controller

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

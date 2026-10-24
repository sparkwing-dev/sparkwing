package nodehost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestBurstRequestsLogicalPermitBeforeNonblockingCompute(t *testing.T) {
	gate, probe, accepted, row := capacityFixture(t)
	var spec runspec.Spec
	if err := json.Unmarshal(accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Request.Request.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here"}, {Target: "cloud"}}}
	accepted.Spec, _ = json.Marshal(spec)
	gate.compute = &LocalAdmission{Daemon: admissionAcquireFunc(func(_ context.Context, request wingwire.AdmissionRequest, _ func(wingwire.Queued)) (Lease, error) {
		if len(probe.bindings) != 1 || !request.NonBlocking || len(request.Semaphores) != 0 {
			t.Fatalf("request=%+v logical requests=%d", request, len(probe.bindings))
		}
		return nil, &wingdclient.AdmissionError{Policy: wingwire.PolicyFail, Key: "capacity"}
	})}
	err := gate.claimedNode(t.Context(), NodeInput{Accepted: accepted, NodeID: string(row.Ref.NodeID), Generation: row.Generation, Execution: row.Execution}, func(context.Context) error { t.Fatal("busy compute executed"); return nil })
	if err != nil || probe.relinquished != 1 {
		t.Fatalf("relinquished=%d error=%v", probe.relinquished, err)
	}
}

func TestBurstComputeIsUntouchedWhileLogicalCapacityWaits(t *testing.T) {
	gate, probe, accepted, row := capacityFixture(t)
	probe.admit = func(context.Context) (runstate.AcquireSlotResponse, error) {
		return runstate.AcquireSlotResponse{Kind: runstate.AcquireSkipped}, nil
	}
	gate.compute = &LocalAdmission{Daemon: admissionAcquireFunc(func(context.Context, wingwire.AdmissionRequest, func(wingwire.Queued)) (Lease, error) {
		t.Fatal("compute preceded admission")
		return nil, nil
	})}
	if err := gate.claimedNode(t.Context(), NodeInput{Accepted: accepted, NodeID: string(row.Ref.NodeID), Generation: row.Generation}, func(context.Context) error { return nil }); err == nil {
		t.Fatal("skip lost")
	}
}

package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestJobStageQueuesAllowCrossRunPipelining(t *testing.T) {
	group := func(name string) func(*nodeproto.PlanDoc) {
		return func(doc *nodeproto.PlanDoc) {
			doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: name, Capacity: new(1)}}
		}
	}
	f := newStoreRecoveryFixtureAt(t, true, storetest.NewSQLite(t), group("production"))
	admit := func(binding runstate.CapacityBinding, actor runstate.Actor, want runstate.AcquireKind) {
		t.Helper()
		if result, err := f.tenant.MayStart(t.Context(), binding, actor); err != nil || result.Kind != want {
			t.Fatalf("admit %s: %+v, %v, want %s", binding.RunID, result, err, want)
		}
	}
	start := func(binding runstate.CapacityBinding, actor runstate.Actor) {
		t.Helper()
		admit(binding, actor, runstate.AcquireGranted)
		ref := runspec.NodeRef{RunID: runspec.RunID(binding.RunID), NodeID: runspec.NodeID(binding.NodeID)}
		if _, err := f.tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: actor, SourceAttempt: acceptanceAttempt(0)}); err != nil {
			t.Fatal(err)
		}
	}
	finish := func(binding runstate.CapacityBinding, actor runstate.Actor) {
		t.Helper()
		ref := runspec.NodeRef{RunID: runspec.RunID(binding.RunID), NodeID: runspec.NodeID(binding.NodeID)}
		if _, err := f.tenant.TransitionNode(t.Context(), store.ReportNode{Ref: ref, Actor: actor, Outcome: runstate.NodeSuccess, SourceAttempt: acceptanceAttempt(1)}); err != nil {
			t.Fatal(err)
		}
	}
	production, prodActor := admissionPolicyBlocker(t, f)
	admit(production, prodActor, runstate.AcquireQueued)
	build, buildActor := admissionPolicyBlocker(t, f, group("build"))
	start(build, buildActor)
	finish(build, buildActor)
	staging, stageActor := admissionPolicyBlocker(t, f, group("staging"))
	start(staging, stageActor)
	waiting, waiter := admissionPolicyBlocker(t, f, group("staging"))
	admit(waiting, waiter, runstate.AcquireQueued)
	admit(production, prodActor, runstate.AcquireQueued)
	finish(staging, stageActor)
	start(waiting, waiter)
	admit(production, prodActor, runstate.AcquireQueued)
	finish(runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}, f.holder)
	start(production, prodActor)
	finish(production, prodActor)
	finish(waiting, waiter)
}

package launcher

import (
	"context"
	"slices"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestLauncherRetiresStaleGenerationAfterRestart(t *testing.T) {
	l, kube, boundary, prior := fixture()
	kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		job.UID = types.UID(job.Name)
		return false, nil, nil
	})
	unit := wireAward(capability.UnitNode)
	unit.Capability = ""
	stale, err := cloudBinding(unit, capability.DeliveryBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	prior.award.Binding, prior.award.Engine = stale, unit.Engine
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	unit.Binding.Generation++
	current, err := cloudBinding(unit, capability.DeliveryBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	prior.award.Binding = current
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	control, err := NewControllerControl(launcherWireFake{sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
		if len(request.Observed) != 2 {
			t.Fatal("restart lost a persisted Job")
		}
		response := controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{unit}}
		for _, observation := range request.Observed {
			if observation.Award.Generation == stale.Generation {
				response.Retired = append(response.Retired, observation.Award)
			}
		}
		return response, nil
	}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{unit.Unit}})
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Launcher{Kube: kube, Config: l.Config, Boundary: boundary, Control: control}
	if err := restarted.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := kube.BatchV1().Jobs(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].Name != jobName(current) || boundary.releases != 1 {
		t.Fatalf("replacement Job was not retained: jobs=%+v releases=%d error=%v", jobs, boundary.releases, err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "jobs" {
			options := action.(ktesting.DeleteAction).GetDeleteOptions()
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != types.UID(jobName(stale)) {
				t.Fatal("cleanup did not retain the stale Job UID precondition")
			}
		}
	}
}

func FuzzControllerSyncSeparatesClaimGenerations(f *testing.F) {
	for kind := range uint8(3) {
		f.Add(kind, uint16(1), uint16(1), false)
		f.Add(kind, uint16(65535), uint16(7), true)
	}
	f.Fuzz(func(t *testing.T, kind uint8, generation, step uint16, reverse bool) {
		unit := wireAward([]capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}[kind%3])
		unit.Request.RunID = "original-run"
		if unit.Accepted != nil {
			unit.Accepted.Spec.Request = unit.Request
		}
		unit.Capability = ""
		if unit.Unit == capability.UnitEval {
			unit.Binding.Expansion.Generation = int64(generation) + int64(step) + 2
		} else {
			unit.Binding.Generation = int64(generation) + int64(step) + 2
		}
		current, err := cloudBinding(unit, capability.DeliveryBootstrap)
		if err != nil {
			t.Fatal(err)
		}
		stale := current
		stale.Generation = int64(generation) + 1
		observations := []CloudJobObservation{
			{Binding: current, JobNamespace: "cloud", JobName: jobName(current), JobUID: "current-job", NodeUID: "current-node"},
			{Binding: stale, JobNamespace: "cloud", JobName: jobName(stale), JobUID: "stale-job", NodeUID: "stale-node"},
		}
		if reverse {
			slices.Reverse(observations)
		}
		calls := 0
		control, err := NewControllerControl(launcherWireFake{sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			calls++
			if len(request.Observed) != 2 {
				t.Fatal("sync did not retain both Jobs")
			}
			var remove capability.LeaseRef
			for i, observation := range request.Observed {
				if observation.Award.Generation != observations[i].Binding.Generation || observation.Award.Holder != observations[i].Binding.Holder || observation.Award.ID != unit.UnitID || observation.Award.Kind != unit.Unit {
					t.Fatal("sync changed a persisted award")
				}
				if observation.Award.Generation == stale.Generation {
					remove = observation.Award
				}
			}
			return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{unit}, Retired: []capability.LeaseRef{remove}}, nil
		}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{unit.Unit}})
		if err != nil {
			t.Fatal(err)
		}
		states, err := control.Sync(t.Context(), observations)
		if err != nil || len(states) != 2 || calls != 1 {
			t.Fatalf("sync lost an award generation: states=%+v calls=%d error=%v", states, calls, err)
		}
		for i, state := range states {
			if !sameBinding(state.Binding, observations[i].Binding) || state.Delete != (state.Binding.Generation == stale.Generation) {
				t.Fatal("sync applied cleanup to another generation")
			}
		}
	})
}

func FuzzControllerSyncRequiresExactlyObservedDecisions(f *testing.F) {
	for kind := range uint8(3) {
		for mode := range uint8(4) {
			f.Add(kind, mode, uint16(1), false)
		}
	}
	f.Fuzz(func(t *testing.T, kind, mode uint8, generation uint16, reverse bool) {
		unit := wireAward([]capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}[kind%3])
		unit.Capability = ""
		binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
		if err != nil {
			t.Fatal(err)
		}
		observation := CloudJobObservation{Binding: binding, JobNamespace: "cloud", JobName: jobName(binding), JobUID: "job-uid", NodeUID: "node-uid"}
		control, err := NewControllerControl(launcherWireFake{sync: func(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			response := controllerwire.LauncherSyncResponse{}
			if mode%2 == 0 {
				response.Awards = append(response.Awards, unit)
			} else {
				response.Retired = []capability.LeaseRef{observation.Binding.LeaseRef()}
			}
			if mode%4 < 2 {
				foreign := wireAward(unit.Unit)
				foreign.Capability = ""
				if foreign.Unit == capability.UnitEval {
					foreign.Binding.Expansion.Generation += int64(generation) + 1
				} else {
					foreign.Binding.Generation += int64(generation) + 1
				}
				response.Awards = append(response.Awards, foreign)
			}
			if reverse {
				slices.Reverse(response.Awards)
			}
			return response, nil
		}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{unit.Unit}})
		if err != nil {
			t.Fatal(err)
		}
		states, err := control.Sync(t.Context(), []CloudJobObservation{observation})
		if mode%4 < 2 {
			if err == nil || len(states) != 0 {
				t.Fatalf("unobserved award accepted: states=%+v error=%v", states, err)
			}
		} else if err != nil || len(states) != 1 || !sameBinding(states[0].Binding, binding) || states[0].Delete != (mode%2 == 1) {
			t.Fatalf("observed decision refused: states=%+v error=%v", states, err)
		}
	})
}

func TestControllerSyncDeferredDecisionRequiresExactObservedFence(t *testing.T) {
	unit := wireAward(capability.UnitNode)
	binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	observation := CloudJobObservation{Binding: binding, JobNamespace: "cloud", JobName: jobName(binding), JobUID: "job-uid", NodeUID: "node-uid"}
	for _, foreign := range []bool{false, true} {
		control, err := NewControllerControl(launcherWireFake{sync: func(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			fence := binding.LeaseRef()
			if foreign {
				fence.Generation++
			}
			return controllerwire.LauncherSyncResponse{Deferred: []capability.LeaseRef{fence}}, nil
		}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{unit.Unit}})
		if err != nil {
			t.Fatal(err)
		}
		states, err := control.Sync(t.Context(), []CloudJobObservation{observation})
		if foreign {
			if err == nil || len(states) != 0 {
				t.Fatalf("foreign deferred decision accepted: %+v, %v", states, err)
			}
		} else if err != nil || len(states) != 1 || !states[0].Deferred || states[0].Delete || !sameBinding(states[0].Binding, binding) {
			t.Fatalf("deferred decision=%+v, %v", states, err)
		}
	}
}

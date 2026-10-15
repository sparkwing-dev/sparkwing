package launcher

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type launcherWireFake struct {
	claim func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
	sync  func(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error)
}

func (f launcherWireFake) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return f.claim(ctx, request)
}

func (f launcherWireFake) SyncLauncher(ctx context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	return f.sync(ctx, request)
}

func wireAward(kind capability.UnitKind) controllerwire.ClaimedUnit {
	deadline := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	identity := engine.Identity{Version: "v1.2.3", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}, ManifestDigest: strings.Repeat("a", 64)}
	unit := controllerwire.ClaimedUnit{
		Unit: kind, Purpose: capability.PurposeWork, Delivery: capability.DeliveryBootstrap,
		UnitID: "awarded-unit", RunID: "run", JobID: "job", Capability: "one-time-bootstrap",
		Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "server-holder", Generation: 3},
		Request: runspec.SealedRequest{RunID: "run", Request: runspec.Request{Team: "awarded-team"}},
		Execution: &runspec.JobExecutionSettings{Resources: runspec.Setting[runspec.Resources]{Optional: runspec.Optional[runspec.Resources]{Set: true, Value: runspec.Resources{
			Cores:       runspec.Setting[float64]{Optional: runspec.Optional[float64]{Set: true, Value: 1.5}},
			MemoryBytes: runspec.Setting[int64]{Optional: runspec.Optional[int64]{Set: true, Value: 2 << 30}},
		}}}},
		OwnerDeadline: &deadline, AttemptDeadline: &deadline, ParentCapabilityExpiry: &deadline, Engine: &identity,
	}
	unit.Request.Request.Git = runspec.Git{Repository: runspec.Repository{Key: "git:github.com/team/repo"}, SHA: strings.Repeat("a", 40)}
	unit.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://github.com/team/repo", PipelineDir: ".sparkwing"}
	unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: unit.Request, Resolved: runspec.Resolved{Engine: identity}}}
	switch kind {
	case capability.UnitPlan:
		unit.Purpose, unit.Binding.Kind = capability.PurposePlan, capability.BindingRun
		unit.JobID, unit.Binding.NodeID = "", ""
		unit.Accepted, unit.Engine = nil, nil
	case capability.UnitEval:
		unit.Purpose, unit.SourceGeneration = capability.PurposeEval, 7
		unit.Binding.Generation, unit.Binding.Holder = 7, "source-holder"
		unit.Binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: unit.UnitID, Generation: 3, Holder: "server-holder"}
	}
	return unit
}

func TestControllerClaimPreservesAwardWithoutRetry(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			unit := wireAward(kind)
			demand := controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{kind}, Labels: []string{"os=linux"}, AvailableCores: 4, AvailableMemoryBytes: 8 << 30}
			calls := 0
			control, err := NewControllerControl(launcherWireFake{claim: func(ctx context.Context, got controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				if ctx != t.Context() || !reflect.DeepEqual(got, demand) {
					t.Fatalf("claim changed context or observations: %+v", got)
				}
				return controllerwire.ClaimResponse{Claim: &unit}, nil
			}}, demand)
			if err != nil {
				t.Fatal(err)
			}
			var projected source.Spec
			if kind == capability.UnitPlan {
				projected, err = source.ForRequest(unit.Request)
			} else {
				projected, err = source.ForExecution(unit.Accepted.Spec)
			}
			if err != nil {
				t.Fatal(err)
			}
			award, err := control.Claim(t.Context())
			if err != nil || award == nil || calls != 1 {
				t.Fatalf("claim=%+v calls=%d err=%v", award, calls, err)
			}
			if !reflect.DeepEqual(award.Source, projected) || award.Binding.Team != "awarded-team" || award.Binding.UnitID != unit.UnitID || award.Binding.Holder != "server-holder" || award.Binding.Generation != 3 || award.Binding.Attempt != 0 || string(award.Bootstrap) != unit.Capability || award.CPUCores != 1.5 || award.Memory != 2<<30 || !reflect.DeepEqual(award.Engine, unit.Engine) {
				t.Fatalf("award lost execution inputs: %+v", award.Binding)
			}
			if kind == capability.UnitEval && (award.Binding.SourceNodeID != "job" || award.Binding.SourceGeneration != 7) {
				t.Fatal("eval lost its source fence")
			}
		})
	}
	failure := errors.New("lost claim response")
	calls := 0
	control, err := NewControllerControl(launcherWireFake{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
		calls++
		return controllerwire.ClaimResponse{}, failure
	}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
	if err != nil {
		t.Fatal(err)
	}
	if award, err := control.Claim(t.Context()); award != nil || !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("lost one-time delivery was replayed: calls=%d err=%v", calls, err)
	}
}

func FuzzControllerRetryAwardPreservesSealsAndFence(f *testing.F) {
	for kind := uint8(0); kind < 3; kind++ {
		for mutation := uint8(0); mutation < 5; mutation++ {
			f.Add(kind, []byte("original-run"), mutation)
		}
	}
	f.Fuzz(func(t *testing.T, kind uint8, origin []byte, mutation uint8) {
		unit := wireAward([]capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}[kind%3])
		unit.Request.RunID = "source-" + hex.EncodeToString(origin)
		unit.Request.Hash = "request-hash"
		if unit.Accepted != nil {
			unit.Accepted.Spec.Request = unit.Request
		}
		control, err := NewControllerControl(launcherWireFake{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			return controllerwire.ClaimResponse{Claim: &unit}, nil
		}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{unit.Unit}})
		if err != nil {
			t.Fatal(err)
		}
		award, err := control.Claim(t.Context())
		if err != nil || award == nil || award.Binding.RunID != unit.RunID || award.Source.RepoKey != string(unit.Request.Request.Git.Repository.Key) || award.Source.SHA != unit.Request.Request.Git.SHA {
			t.Fatalf("retry lost its execution fence or original source: award=%+v error=%v", award, err)
		}
		mutation %= 5
		switch mutation {
		case 1:
			unit.Binding.RunID = unit.Request.RunID
		case 2, 3, 4:
			if unit.Accepted == nil {
				unit.Request.RunID = ""
			} else {
				switch mutation {
				case 2:
					unit.Accepted.Spec.Request.RunID += "foreign"
				case 3:
					unit.Accepted.Spec.Request.Hash += "foreign"
				case 4:
					unit.Accepted.Spec.Request.Request.Team += "foreign"
				}
			}
		}
		if _, err := control.Claim(t.Context()); (err != nil) != (mutation != 0) {
			t.Fatalf("claim mutation=%d error=%v", mutation, err)
		}
		exchanged := unit
		exchanged.Delivery, exchanged.Capability, exchanged.LeaseExpiresAt = capability.DeliveryDirect, "exchanged", *unit.ParentCapabilityExpiry
		hosted := false
		host := BootstrapHost{ControllerURL: "https://controller.example", NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
			return bootstrapController{exchange: func(context.Context) (controllerwire.ExchangeResponse, error) {
				return controllerwire.ExchangeResponse{Claim: exchanged}, nil
			}}, nil
		}, Host: bootstrapUnitHost(func(_ context.Context, got controllerwire.ClaimedUnit) error {
			hosted = true
			if got.RunID != unit.RunID || got.Request.RunID != unit.Request.RunID || !reflect.DeepEqual(got.Binding, unit.Binding) {
				t.Fatal("bootstrap lost the original seals or execution fence")
			}
			return nil
		})}
		if err := host.Run(t.Context(), *award); hosted != (mutation == 0) || (err != nil) != (mutation != 0) {
			t.Fatalf("exchange mutation=%d hosted=%v error=%v", mutation, hosted, err)
		}
	})
}

func TestControllerSyncUsesPersistedObservationsAndExactFences(t *testing.T) {
	unit := wireAward(capability.UnitNode)
	binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	observation := CloudJobObservation{Binding: binding, JobNamespace: "cloud", JobName: jobName(binding), JobUID: "job-uid", NodeUID: "node-uid"}
	for _, test := range []struct {
		name           string
		response       func() controllerwire.LauncherSyncResponse
		remove, reject bool
	}{
		{name: "live", response: func() controllerwire.LauncherSyncResponse {
			u := unit
			u.Capability = ""
			return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{u}}
		}},
		{name: "retired", remove: true, response: func() controllerwire.LauncherSyncResponse {
			return controllerwire.LauncherSyncResponse{Remove: []controllerwire.LauncherJobRef{{Namespace: observation.JobNamespace, Name: observation.JobName, UID: observation.JobUID}}}
		}},
		{name: "missing", reject: true, response: func() controllerwire.LauncherSyncResponse { return controllerwire.LauncherSyncResponse{} }},
		{name: "bearer", reject: true, response: func() controllerwire.LauncherSyncResponse {
			return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{unit}}
		}},
		{name: "changed_fence", reject: true, response: func() controllerwire.LauncherSyncResponse {
			u := unit
			u.Capability = ""
			u.Binding.Generation++
			return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{u}}
		}},
		{name: "contradiction", reject: true, response: func() controllerwire.LauncherSyncResponse {
			u := unit
			u.Capability = ""
			return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{u}, Remove: []controllerwire.LauncherJobRef{{Namespace: observation.JobNamespace, Name: observation.JobName, UID: observation.JobUID}}}
		}},
		{name: "unknown_cleanup", reject: true, response: func() controllerwire.LauncherSyncResponse {
			return controllerwire.LauncherSyncResponse{Remove: []controllerwire.LauncherJobRef{{Namespace: observation.JobNamespace, Name: "some-other-job", UID: observation.JobUID}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			control, err := NewControllerControl(launcherWireFake{sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
				want := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: unit.Unit, ID: unit.UnitID, Holder: binding.Holder, Generation: binding.Generation}, Job: controllerwire.LauncherJobRef{Namespace: observation.JobNamespace, Name: observation.JobName, UID: observation.JobUID}, NodeUID: observation.NodeUID}
				if !slices.Equal(request.Observed, []controllerwire.LauncherObservation{want}) {
					t.Fatalf("observation=%+v", request.Observed)
				}
				return test.response(), nil
			}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
			if err != nil {
				t.Fatal(err)
			}
			states, err := control.Sync(t.Context(), []CloudJobObservation{observation})
			if test.reject {
				if err == nil || len(states) != 0 || strings.Contains(err.Error(), unit.Capability) {
					t.Fatalf("unsafe sync result: %+v err=%v", states, err)
				}
				return
			}
			if err != nil || len(states) != 1 || !sameBinding(states[0].Binding, binding) || states[0].Delete != test.remove {
				t.Fatalf("states=%+v err=%v", states, err)
			}
		})
	}
}

func TestControllerClaimRejectsIncompleteOrConflictingAwards(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*controllerwire.ClaimedUnit)
	}{
		{"direct_credential", func(u *controllerwire.ClaimedUnit) { u.Delivery = capability.DeliveryDirect }},
		{"missing_bootstrap", func(u *controllerwire.ClaimedUnit) { u.Capability = "" }},
		{"missing_unit", func(u *controllerwire.ClaimedUnit) { u.UnitID = "" }},
		{"missing_holder", func(u *controllerwire.ClaimedUnit) { u.Binding.Holder = "" }},
		{"different_run", func(u *controllerwire.ClaimedUnit) { u.Binding.RunID = "other-run" }},
		{"different_job", func(u *controllerwire.ClaimedUnit) { u.Binding.NodeID = "other-job" }},
		{"negative_attempt", func(u *controllerwire.ClaimedUnit) { u.Binding.Attempt = -1 }},
		{"missing_parent_expiry", func(u *controllerwire.ClaimedUnit) { u.ParentCapabilityExpiry = nil }},
		{"missing_resources", func(u *controllerwire.ClaimedUnit) { u.Execution = nil }},
		{"unset_cores", func(u *controllerwire.ClaimedUnit) { u.Execution.Resources.Value.Cores.Set = false }},
		{"missing_accepted_spec", func(u *controllerwire.ClaimedUnit) { u.Accepted = nil }},
		{"missing_engine", func(u *controllerwire.ClaimedUnit) { u.Engine = nil }},
		{"different_engine", func(u *controllerwire.ClaimedUnit) { u.Engine.Version = "v2.0.0" }},
		{"unexpected_source_generation", func(u *controllerwire.ClaimedUnit) { u.SourceGeneration = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			unit := wireAward(capability.UnitNode)
			test.change(&unit)
			control, err := NewControllerControl(launcherWireFake{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				return controllerwire.ClaimResponse{Claim: &unit}, nil
			}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
			if err != nil {
				t.Fatal(err)
			}
			if award, err := control.Claim(t.Context()); award != nil || err == nil || strings.Contains(err.Error(), "one-time-bootstrap") {
				t.Fatalf("invalid award accepted or exposed: %v", err)
			}
		})
	}
}

func TestControllerControlCapturesDemandAndHonorsCancellation(t *testing.T) {
	for _, kinds := range [][]capability.UnitKind{nil, {capability.UnitNode, capability.UnitNode}, {capability.UnitTrigger}, {"unknown"}} {
		if _, err := NewControllerControl(launcherWireFake{}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: kinds}); err == nil {
			t.Fatalf("invalid launcher kinds accepted: %v", kinds)
		}
	}
	demand := controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}, Labels: []string{"os=linux"}}
	calls := 0
	control, err := NewControllerControl(launcherWireFake{claim: func(_ context.Context, got controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
		calls++
		if got.Kinds[0] != capability.UnitNode || got.Labels[0] != "os=linux" {
			t.Fatal("caller or transport mutated captured claim observations")
		}
		got.Kinds[0], got.Labels[0] = capability.UnitTrigger, "os=windows"
		return controllerwire.ClaimResponse{}, nil
	}}, demand)
	if err != nil {
		t.Fatal(err)
	}
	demand.Kinds[0], demand.Labels[0] = capability.UnitPlan, "os=darwin"
	for range 2 {
		if _, err := control.Claim(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("launcher stopped")
	cancel(cause)
	if _, err := control.Claim(ctx); !errors.Is(err, cause) || calls != 2 {
		t.Fatalf("cancelled claim reached transport: calls=%d err=%v", calls, err)
	}
	if _, err := control.Sync(ctx, nil); !errors.Is(err, cause) {
		t.Fatalf("cancelled sync reached transport: %v", err)
	}
}

func TestControllerEvalSyncPreservesBothGenerations(t *testing.T) {
	for _, change := range []string{"none", "source", "lease", "holder", "missing_lease", "wrong_lease"} {
		t.Run(change, func(t *testing.T) {
			unit := wireAward(capability.UnitEval)
			binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
			if err != nil {
				t.Fatal(err)
			}
			unit.Capability = ""
			switch change {
			case "source":
				unit.SourceGeneration++
				unit.Binding.Generation++
			case "lease":
				unit.Binding.Expansion.Generation++
			case "holder":
				unit.Binding.Expansion.Holder = "different-holder"
			case "missing_lease":
				unit.Binding.Expansion = nil
			case "wrong_lease":
				unit.Binding.Expansion.ID = "another-eval"
			}
			control, err := NewControllerControl(launcherWireFake{sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
				want := capability.LeaseRef{Kind: capability.UnitEval, ID: binding.UnitID, Holder: binding.Holder, Generation: binding.Generation}
				if len(request.Observed) != 1 || request.Observed[0].Award != want {
					t.Fatalf("eval observation lost its live award: %+v", request.Observed)
				}
				return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{unit}}, nil
			}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitEval}})
			if err != nil {
				t.Fatal(err)
			}
			states, err := control.Sync(t.Context(), []CloudJobObservation{{Binding: binding, JobNamespace: "cloud", JobName: jobName(binding), JobUID: "job-uid", NodeUID: "node-uid"}})
			if change == "none" {
				if err != nil || len(states) != 1 || states[0].Binding.Generation != 3 || states[0].Binding.SourceGeneration != 7 {
					t.Fatalf("live eval lost its two fences: %+v err=%v", states, err)
				}
			} else if err == nil || len(states) != 0 {
				t.Fatalf("changed eval fence accepted: %+v err=%v", states, err)
			}
		})
	}
}

func TestLauncherObservesCreatedJobIdentity(t *testing.T) {
	l, kube, _, control := fixture()
	kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		job.UID = "created-job-uid"
		return false, nil, nil
	})
	calls := 0
	l.Control = syncControl{control, func(observations []CloudJobObservation) ([]CloudJobState, error) {
		calls++
		if len(observations) != 1 || observations[0].JobNamespace != "cloud" || observations[0].JobUID != "created-job-uid" || observations[0].NodeUID != "uid" {
			t.Fatalf("lost observed Job or node identity: %+v", observations)
		}
		return []CloudJobState{{Binding: observations[0].Binding}}, nil
	}}
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("observations=%d, want creation and restart reconciliation", calls)
	}
}

func FuzzControllerCleanupRequiresObservedJobIdentity(f *testing.F) {
	for _, field := range []uint8{0, 1, 2} {
		f.Add(field, "replacement")
		f.Add(field, "")
	}
	f.Fuzz(func(t *testing.T, field uint8, replacement string) {
		unit := wireAward(capability.UnitNode)
		binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
		if err != nil {
			t.Fatal(err)
		}
		observed := CloudJobObservation{Binding: binding, JobNamespace: "cloud", JobName: jobName(binding), JobUID: "job-uid", NodeUID: "node-uid"}
		original := controllerwire.LauncherJobRef{Namespace: observed.JobNamespace, Name: observed.JobName, UID: observed.JobUID}
		remove := original
		switch field % 3 {
		case 0:
			remove.Namespace = replacement
		case 1:
			remove.Name = replacement
		case 2:
			remove.UID = replacement
		}
		control, err := NewControllerControl(launcherWireFake{sync: func(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			return controllerwire.LauncherSyncResponse{Remove: []controllerwire.LauncherJobRef{remove}}, nil
		}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
		if err != nil {
			t.Fatal(err)
		}
		states, err := control.Sync(t.Context(), []CloudJobObservation{observed})
		if remove != original {
			if err == nil || len(states) != 0 {
				t.Fatalf("unobserved Job selected for cleanup: %+v, %v", states, err)
			}
		} else if err != nil || len(states) != 1 || !states[0].Delete {
			t.Fatalf("observed Job cleanup refused: %+v, %v", states, err)
		}
	})
}

func TestCloudJobNamesSeparateAwardedUnits(t *testing.T) {
	unit := wireAward(capability.UnitEval)
	first, err := cloudBinding(unit, capability.DeliveryBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.UnitID = "another-awarded-unit"
	if jobName(first) == jobName(second) {
		t.Fatal("different awarded units alias the same Kubernetes Job")
	}
	second = first
	second.SourceGeneration++
	if jobName(first) == jobName(second) {
		t.Fatal("different terminal source generations alias the same Kubernetes Job")
	}
}

func FuzzControllerSyncRetainsObservedIdentity(f *testing.F) {
	for field := range uint8(3) {
		f.Add(field, "replacement")
		f.Add(field, "")
	}
	f.Fuzz(func(t *testing.T, field uint8, replacement string) {
		unit := wireAward(capability.UnitNode)
		binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
		if err != nil {
			t.Fatal(err)
		}
		observation := CloudJobObservation{Binding: binding, JobNamespace: "cloud", JobName: jobName(binding), JobUID: "job-uid", NodeUID: "node-uid"}
		original := controllerwire.LauncherJobRef{Namespace: observation.JobNamespace, Name: observation.JobName, UID: observation.JobUID}
		var removed controllerwire.LauncherJobRef
		control, err := NewControllerControl(launcherWireFake{sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			switch field % 3 {
			case 0:
				request.Observed[0].Job.Namespace = replacement
			case 1:
				request.Observed[0].Job.Name = replacement
			case 2:
				request.Observed[0].Job.UID = replacement
			}
			removed = request.Observed[0].Job
			return controllerwire.LauncherSyncResponse{Remove: []controllerwire.LauncherJobRef{removed}}, nil
		}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
		if err != nil {
			t.Fatal(err)
		}
		states, err := control.Sync(t.Context(), []CloudJobObservation{observation})
		valid := removed == original
		if (err == nil) != valid || (len(states) == 1) != valid {
			t.Fatal("transport changed the persisted Job identity used for cleanup")
		}
	})
}

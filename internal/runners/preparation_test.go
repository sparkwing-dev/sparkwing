package runners

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestNodePreparationAcquiresAcceptedSourceUnderKeeper(t *testing.T) {
	unit := hostingAward(capability.UnitNode)
	unit.Accepted.Plan = []byte(`{"jobs":[{"id":"job"}]}`)
	unit.Accepted.Spec.Hash, unit.Accepted.PlanHash = "sealed-spec", "sealed-plan"
	unit.Execution = &runspec.JobExecutionSettings{}
	entry := hostingInput(unit).Entry
	var events []string
	preparation := NodePreparation{
		Source: source.Options{Workspace: "/workspace"},
		Input:  nodehost.NodeInput{SpecHash: "job-hash", RouteBinding: nodeproto.ProcessBinding{RunID: unit.RunID, JobID: unit.JobID, Routes: nodeproto.ProcessRoutes{URL: "http://node", Bearer: "delegated-node"}}},
		Host: nodeHostFunc(func(ctx context.Context, in nodehost.NodeInput) error {
			events = append(events, "execute")
			if ctx.Value(preparationContextKey{}) != true || in.Entry != entry || in.Accepted.SpecHash != "sealed-spec" || in.Accepted.PlanHash != "sealed-plan" || len(in.Accepted.Plan.Jobs) != 1 || in.NodeID != unit.JobID || in.Generation != unit.Binding.Generation || in.Attempt != unit.Binding.Attempt || !reflect.DeepEqual(in.Engine, entry.Engine) {
				t.Error("execution lost its acquired source, accepted seals or keeper context")
			}
			return nil
		}),
		Acquire: func(ctx context.Context, spec runspec.Spec, options source.Options) (*source.Entry, func() error, error) {
			events = append(events, "acquire")
			if ctx.Value(preparationContextKey{}) != true || !reflect.DeepEqual(spec, unit.Accepted.Spec) || options.Workspace != "/workspace" {
				t.Error("acquisition did not use the accepted spec under the keeper")
			}
			return entry, func() error { events = append(events, "source-close"); return nil }, nil
		},
		Close: func() error { events = append(events, "services-close"); return nil },
	}
	host := Hosting{
		ControllerURL: "http://controller", Lease: time.Minute, Interval: time.Second,
		NewController: hostingClient(func(context.Context) (lease.Beat, error) { events = append(events, "renew"); return lease.Beat{}, nil }),
		StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) {
			return context.WithValue(ctx, preparationContextKey{}, true), func() { events = append(events, "join") }
		},
		PrepareNode: preparation.Prepare,
	}
	if err := host.Host(t.Context(), unit); err != nil {
		t.Fatal(err)
	}
	if want := []string{"renew", "acquire", "execute", "join", "source-close", "services-close"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events %v, want %v", events, want)
	}
}

type preparationContextKey struct{}

func TestNodePreparationClosesPartialAcquisition(t *testing.T) {
	unit := hostingAward(capability.UnitNode)
	unit.Accepted.Plan = []byte(`{"jobs":[]}`)
	unit.Execution = &runspec.JobExecutionSettings{}
	failure, sourceFailure, servicesFailure := errors.New("acquire"), errors.New("source close"), errors.New("services close")
	preparation := NodePreparation{
		Host:  nodeHostFunc(func(context.Context, nodehost.NodeInput) error { t.Fatal("executed failed acquisition"); return nil }),
		Input: nodehost.NodeInput{SpecHash: "job-hash", RouteBinding: nodeproto.ProcessBinding{RunID: unit.RunID, JobID: unit.JobID, Routes: nodeproto.ProcessRoutes{URL: "http://node", Bearer: "delegated-node"}}},
		Acquire: func(context.Context, runspec.Spec, source.Options) (*source.Entry, func() error, error) {
			return nil, func() error { return sourceFailure }, failure
		},
		Close: func() error { return servicesFailure },
	}
	_, _, closeSource, err := preparation.Prepare(t.Context(), unit, nil)
	if !errors.Is(err, failure) || closeSource == nil {
		t.Fatalf("partial source: cleanup present=%v error=%v", closeSource != nil, err)
	}
	err = closeSource()
	if !errors.Is(err, sourceFailure) || !errors.Is(err, servicesFailure) {
		t.Fatalf("lost cleanup failure: %v", err)
	}
}

func FuzzNodePreparationRejectsForeignRoutes(f *testing.F) {
	f.Add("run", "job", "delegated-node", false)
	f.Add("other", "job", "delegated-node", false)
	f.Add("run", "other", "delegated-node", false)
	f.Add("run", "job", "award-secret", false)
	f.Add("run", "job", "delegated-node", true)
	f.Fuzz(func(t *testing.T, run, job, bearer string, foreignEngine bool) {
		unit := hostingAward(capability.UnitNode)
		unit.Accepted.Plan = []byte(`{"jobs":[]}`)
		unit.Execution = &runspec.JobExecutionSettings{}
		acquired := false
		preparation := NodePreparation{
			Host:  nodeHostFunc(func(context.Context, nodehost.NodeInput) error { return nil }),
			Input: nodehost.NodeInput{SpecHash: "hash", Generation: 999, Attempt: 999, GrantedRunner: nodeproto.RunnerIdentity{Name: "ambient"}, RouteBinding: nodeproto.ProcessBinding{RunID: run, JobID: job, Routes: nodeproto.ProcessRoutes{URL: "http://node", Bearer: bearer}}},
			Acquire: func(context.Context, runspec.Spec, source.Options) (*source.Entry, func() error, error) {
				acquired = true
				entry := hostingInput(unit).Entry
				if foreignEngine {
					entry.Engine.Identity.Version = "v9.9.9"
				}
				return entry, nil, nil
			},
		}
		_, input, cleanup, err := preparation.Prepare(t.Context(), unit, nil)
		if cleanup != nil {
			if err := cleanup(); err != nil {
				t.Fatal(err)
			}
		}
		routeValid := run == unit.RunID && job == unit.JobID && bearer != "" && bearer != unit.Capability
		valid := routeValid && !foreignEngine
		if valid != (err == nil) || acquired != routeValid {
			t.Fatalf("route fence: valid=%v acquired=%v error=%v", valid, acquired, err)
		}
		if valid && (input.NodeID != unit.JobID || input.Generation != unit.Binding.Generation || input.Attempt != unit.Binding.Attempt || input.GrantedRunner.Name != unit.Runner.Name) {
			t.Fatal("prepared input changed its award fence")
		}
	})
}

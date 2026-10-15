package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func nativeStoreFixtureW8(t *testing.T, kind capability.UnitKind) (*controller.EngineController, capability.Issued) {
	t.Helper()
	server, root := newStoredRunnerW8(t, kind)
	tenant, err := server.Store.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	spec := *run.Spec
	spec.PlanHash = "plan"
	body, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Store.DB().ExecContext(t.Context(), `UPDATE runs SET spec_json=? WHERE id='run'`, body); err != nil {
		t.Fatal(err)
	}
	if kind == capability.UnitNode {
		if _, err := server.Store.DB().ExecContext(t.Context(), `UPDATE nodes SET spec_hash='job-hash' WHERE run_id='run'`); err != nil {
			t.Fatal(err)
		}
	}
	return server, root
}

func nativeStoreAwardW8(t *testing.T, kind capability.UnitKind) (*store.Store, controllerwire.ClaimedUnit) {
	t.Helper()
	server, root := nativeStoreFixtureW8(t, kind)
	claimant, err := storedRunnerClientW8(t.Context(), server, root.Raw)
	if err != nil {
		t.Fatal(err)
	}
	var unit controllerwire.ClaimedUnit
	for _, requested := range []capability.UnitKind{capability.UnitTrigger, kind} {
		if unit.Unit == requested {
			break
		}
		response, err := claimant.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{requested}, AvailableCores: 2, AvailableMemoryBytes: 1 << 30})
		if err != nil || response.Claim == nil {
			t.Fatalf("claim %s: %v %+v", requested, err, response.Claim)
		}
		unit = *response.Claim
	}
	unit.Runner.Labels = []string{"registered"}
	return server.Store, unit
}

type nativeStoreEnginesW8 struct{}

func (nativeStoreEnginesW8) Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected engine resolution")
}

func (nativeStoreEnginesW8) Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected engine acquisition")
}

func TestNativeStoreOwnerW8(t *testing.T) {
	t.Run("embedded_queue", func(t *testing.T) { checkNativeStoreOwnerW8(t, false) })
	t.Run("award_snapshot", func(t *testing.T) { checkNativeStoreOwnerW8(t, true) })
}

func checkNativeStoreOwnerW8(t *testing.T, direct bool) {
	t.Helper()
	var server *controller.EngineController
	var root capability.Issued
	var s *store.Store
	var unit controllerwire.ClaimedUnit
	if direct {
		s, unit = nativeStoreAwardW8(t, capability.UnitTrigger)
	} else {
		server, root = nativeStoreFixtureW8(t, capability.UnitTrigger)
		s = server.Store
	}
	expected := unit.Runner
	expected.Labels = append([]string{}, unit.Runner.Labels...)
	bound, acquired, closed := false, false, false
	bind := func(ctx context.Context, binding runners.StoreBinding) (runners.NativeServices, error) {
		bound = true
		if binding.Tenant.Team() != store.DefaultTeam || binding.Principal.Capability.Binding.Holder == "" {
			t.Fatal("native service binding lost authenticated tenant or holder")
		}
		authorities, err := nodehost.NewProcessAuthorities(nodehost.ProcessAuthorityDependencies{Parent: s, Bindings: s})
		if err != nil {
			t.Fatal(err)
		}
		router := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
		if direct {
			unit.Runner.Labels[0] = "ambient"
		}
		cfg := nodehost.NativeHostConfig{
			Lease: lease.Keeper{Lease: time.Minute, Interval: time.Second}, Source: source.Options{Engines: nativeStoreEnginesW8{}},
			Masks: nodehost.NewMaskRegistry(secrets.NewMasker()), Router: router,
			Processes: &nodehost.NativeProcesses{Authorities: authorities, Log: func(context.Context, string, string, nodeproto.FrameLog) error { return nil }},
			Planner:   nodehost.SDKPlanner{Log: func(context.Context, nodeproto.FrameLog) error { return nil }},
			Executor: nodehost.ExecutorDependencies{Bind: func(context.Context, nodehost.NodeInput) (nodehost.ProcessGrant, error) {
				return nodehost.ProcessGrant{}, errors.New("unexpected work")
			}},
			Evaluation: nodehost.EvaluationDependencies{
				Bind: func(context.Context, nodehost.EvaluationInput) (nodehost.ProcessGrant, error) {
					return nodehost.ProcessGrant{}, errors.New("unexpected evaluation")
				},
				ReadOutput: func(context.Context, store.NodeRef, int64) (json.RawMessage, error) {
					return nil, errors.New("unexpected output read")
				},
			},
			Owner: nodehost.OwnerHost{
				Wait: func(context.Context, store.RunSnapshot) error { return errors.New("unexpected state wait") },
				Acquire: func(ctx context.Context, input nodehost.RunInput) (*source.Entry, error) {
					acquired = true
					if direct && !reflect.DeepEqual(input.GrantedRunner, expected) || input.GrantedRunner.Name != "runner" || input.Owner.Authority != binding.Principal.Authority || input.Owner.Owner.Generation != binding.Principal.Capability.Binding.Generation {
						t.Error("native owner received a changed runner or fence")
					}
					if _, err := binding.Tenant.ReadSnapshot(ctx, input.RunID); err != nil {
						t.Error(err)
					}
					var spec runspec.Spec
					if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
						return nil, err
					}
					return &source.Entry{Engine: engine.Resolved{Identity: spec.Resolved.Engine}}, nil
				},
			},
		}
		return runners.NativeServices{Config: cfg, Close: func() error { closed = true; return authorities.Close() }}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var executionErr error
	if direct {
		executionErr = (runners.StoreHosting{Store: s, Bind: bind}).Host(ctx, unit)
	} else {
		runner, err := (localcontroller.Binding{Store: s, Controller: server}).NewQueue(runners.StoreRunnerConfig{
			Bearer:          func(context.Context) (string, error) { return root.Raw, nil },
			Loop:            runners.LoopConfig{Version: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitTrigger}, MaxConcurrent: 1, MaxClaims: 1},
			Bind:            bind,
			RequestCapacity: func() (float64, int64) { return 2, 1 << 30 },
		})
		if err != nil {
			t.Fatal(err)
		}
		executionErr = runner.Run(ctx)
	}
	if executionErr != nil {
		t.Fatal(executionErr)
	}

	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != store.RunSuccess || !bound || !acquired || !closed {
		t.Fatalf("state=%s bound=%v acquired=%v closed=%v error=%v", run.State, bound, acquired, closed, err)
	}
}

func FuzzNativeStoreAwardW8(f *testing.F) {
	for field := uint8(0); field < 10; field++ {
		f.Add(field, "replacement", int64(4))
	}
	f.Fuzz(func(t *testing.T, field uint8, value string, delta int64) {
		if value == "" {
			value = "replacement"
		}
		s, unit := nativeStoreAwardW8(t, capability.UnitNode)
		switch field % 10 {
		case 0:
			unit.Binding.Holder += value
		case 1:
			unit.Binding.Generation ^= delta | 1
		case 2:
			unit.Binding.Attempt ^= delta | 1
		case 3:
			unit.UnitID += value
		case 4:
			unit.Request.Request.Team += value
		case 5:
			unit.Request.Hash += runspec.Hash(value)
		case 6:
			unit.Accepted.Spec.Hash += runspec.Hash(value)
		case 7:
			unit.Execution.Resources.Set = true
			unit.Execution.Resources.Value.Cores.Set = true
			unit.Execution.Resources.Value.Cores.Value = 3
		case 8:
			unit.Capability += value
		case 9:
			grants := []capability.Grant{capability.NodeReport}
			if delta&1 != 0 {
				grants = append(grants, capability.NodeResult)
			}
			unit.Capability = nativeStoreDelegateW8(t, s, unit.Capability, grants)
		}
		host := runners.StoreHosting{Store: s, Bind: func(context.Context, runners.StoreBinding) (runners.NativeServices, error) {
			t.Fatal("changed award reached native service construction")
			return runners.NativeServices{}, nil
		}}
		if err := host.Host(t.Context(), unit); err == nil {
			t.Fatal("changed award was accepted")
		}
	})
}

func nativeStoreDelegateW8(t *testing.T, s *store.Store, raw string, grants []capability.Grant) string {
	t.Helper()
	principal, err := s.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	c := principal.Capability
	issued, err := s.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: principal.Authority, Audience: c.Audience, Grants: grants, Binding: c.Binding, Repositories: c.Repositories, ExpiresAt: c.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Raw
}

func TestNativeStoreRefusesDelegatedOwnerW8(t *testing.T) {
	s, unit := nativeStoreAwardW8(t, capability.UnitTrigger)
	unit.Capability = nativeStoreDelegateW8(t, s, unit.Capability, []capability.Grant{capability.TriggerHost})
	bound := false
	host := runners.StoreHosting{Store: s, Bind: func(context.Context, runners.StoreBinding) (runners.NativeServices, error) {
		bound = true
		return runners.NativeServices{}, errors.New("delegated owner reached service construction")
	}}
	if err := host.Host(t.Context(), unit); err == nil || bound {
		t.Fatalf("delegated owner: bound=%v error=%v", bound, err)
	}
}

func TestNativeStorePartialBindingW8(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, unit := nativeStoreAwardW8(t, kind)
			failure, closeFailure := errors.New("service bind failed"), errors.New("service close failed")
			closed := 0
			host := runners.StoreHosting{Store: s, Bind: func(context.Context, runners.StoreBinding) (runners.NativeServices, error) {
				return runners.NativeServices{Close: func() error { closed++; return closeFailure }}, failure
			}}
			if err := host.Host(t.Context(), unit); !errors.Is(err, failure) || !errors.Is(err, closeFailure) || closed != 1 {
				t.Fatalf("partial native binding: closed=%d error=%v", closed, err)
			}
		})
	}
}

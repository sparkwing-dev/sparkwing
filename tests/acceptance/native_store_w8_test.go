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
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func nativeStoreAwardW8(t *testing.T, kind capability.UnitKind) (*store.Store, controllerwire.ClaimedUnit) {
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

type nativeStoreClaimW8 struct {
	controllerwire.Controller
	unit controllerwire.ClaimedUnit
}

func (c nativeStoreClaimW8) Claim(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return controllerwire.ClaimResponse{Claim: &c.unit}, nil
}

func (nativeStoreEnginesW8) Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected engine resolution")
}

func (nativeStoreEnginesW8) Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected engine acquisition")
}

func TestNativeStoreOwnerW8(t *testing.T) {
	s, unit := nativeStoreAwardW8(t, capability.UnitTrigger)
	expected := unit.Runner
	expected.Labels = append([]string{}, unit.Runner.Labels...)
	bound, acquired, closed := false, false, false
	host := runners.StoreHosting{Store: s, Bind: func(ctx context.Context, binding runners.StoreBinding) (runners.NativeServices, error) {
		bound = true
		if binding.Tenant.Team() != store.DefaultTeam || binding.Principal.Capability.Binding.Holder != unit.Binding.Holder {
			t.Fatal("native service binding lost authenticated tenant or holder")
		}
		authorities, err := nodehost.NewProcessAuthorities(nodehost.ProcessAuthorityDependencies{Parent: s, Bindings: s})
		if err != nil {
			t.Fatal(err)
		}
		router := controller.NewPolicyRouter(controller.RouteAuthorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
		unit.Runner.Labels[0] = "ambient"
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
			Owner: nodehost.OwnerHost{Wait: func(context.Context, store.RunSnapshot) error { return errors.New("unexpected state wait") },
				Acquire: func(ctx context.Context, input nodehost.RunInput) (*source.Entry, error) {
					acquired = true
					if !reflect.DeepEqual(input.GrantedRunner, expected) || input.Owner.Authority != binding.Principal.Authority || input.Owner.Owner.Generation != unit.Binding.Generation {
						t.Error("native owner received a changed runner or fence")
					}
					if _, err := binding.Tenant.ReadSnapshot(ctx, input.RunID); err != nil {
						t.Error(err)
					}
					return &source.Entry{Engine: engine.Resolved{Identity: *unit.Engine}}, nil
				}},
		}
		return runners.NativeServices{Config: cfg, Close: func() error { closed = true; return authorities.Close() }}, nil
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	runner, err := runners.NewManagedRunner(ctx, runners.RuntimeConfig{
		Controller: client.EngineOptions{URL: "http://local-controller", Bearer: func(context.Context) (string, error) { return "claimant", nil }},
		NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
			return nativeStoreClaimW8{unit: unit}, nil
		},
		Loop:    runners.LoopConfig{Version: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitTrigger}, MaxConcurrent: 1, MaxClaims: 1},
		Hosting: host, Versions: runnerVersionPolicyW8(t, "v0.71.0"), Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), unit.RunID)
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

package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func nativeStoreFixtureW8(t *testing.T, kind capability.UnitKind) (*storedEngineW8, capability.Issued) {
	t.Helper()
	server, root := newStoredRunnerW8(t, kind)
	prepareNativeStoreW8(t, server.Store, kind)
	return server, root
}

func prepareNativeStoreW8(t *testing.T, s *store.Store, kind capability.UnitKind) {
	t.Helper()
	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	spec := *run.Spec
	spec.PlanHash = "plan"
	spec.Resolved.SecretRegistration = runspec.SecretRegistration{Version: 1}
	body, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.Exec(t.Context(), s, `UPDATE runs SET spec_json=? WHERE id='run'`, body); err != nil {
		t.Fatal(err)
	}
	if kind == capability.UnitNode {
		if _, err := teststore.SQL(t, s).ExecContext(t.Context(), `UPDATE nodes SET spec_hash='job-hash' WHERE run_id='run'`); err != nil {
			t.Fatal(err)
		}
	}
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
		response, err := claimant.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{requested}})
		if err != nil || response.Claim == nil {
			t.Fatalf("claim %s: %v %+v", requested, err, response.Claim)
		}
		unit = *response.Claim
	}
	unit.Runner.Labels = []string{"registered"}
	return server.Store, unit
}

type nativeStoreEnginesW8 struct{}

type finishingStoreW9 struct {
	nodehost.NativeHostStore
	finished *bool
}

func (s finishingStoreW9) FinishRunAndUnit(ctx context.Context, request runstate.FinishRunRequest) (runstate.Applied, error) {
	*s.finished = true
	return s.NativeHostStore.FinishRunAndUnit(ctx, request)
}

func (nativeStoreEnginesW8) Resolve(context.Context, engineid.Pin, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected engine resolution")
}

func (nativeStoreEnginesW8) Ensure(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected engine acquisition")
}

func TestNativeStoreOwnerW8(t *testing.T) {
	for _, mode := range []string{"embedded_queue", "award_snapshot", "remote_award", "managed_remote", "local_controller", "remote_renewal", "remote_cancel"} {
		t.Run(mode, func(t *testing.T) { checkNativeStoreOwnerW8(t, mode) })
	}
}

func checkNativeStoreOwnerW8(t *testing.T, mode string) {
	t.Helper()
	direct := mode == "award_snapshot" || mode == "remote_award" || mode == "remote_renewal" || mode == "remote_cancel"
	var server *storedEngineW8
	var root capability.Issued
	var s *store.Store
	var unit controllerwire.ClaimedUnit
	if direct {
		s, unit = nativeStoreAwardW8(t, capability.UnitTrigger)
		server = newStoredEngineW8(s, controller.EngineController{RunnerPolicy: runnerStorePolicyW8{}})
	} else if mode == "embedded_queue" || mode == "managed_remote" {
		server, root = nativeStoreFixtureW8(t, capability.UnitTrigger)
		s = server.Store
	}
	expected := unit.Runner
	expected.Labels = append([]string{}, unit.Runner.Labels...)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	renewed := make(chan struct{})
	var renewals atomic.Int64
	bound, acquired, closed, finishedThroughServices := false, false, false, false
	done := make(chan struct{})
	bind := func(ctx context.Context, binding localcontroller.StoreBinding) (nodehost.NativeServices, error) {
		bound = true
		if binding.Tenant.Team() != store.DefaultTeam || binding.Principal.Capability.Binding.Holder == "" {
			t.Fatal("native service binding lost authenticated tenant or holder")
		}
		authorities, err := nodehost.NewProcessAuthorities(nodehost.ProcessAuthorityDependencies{Parent: s.AsOperator(), Bindings: s.AsOperator()})
		if err != nil {
			t.Fatal(err)
		}
		router := routepolicy.New(routepolicy.Authorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
		if direct {
			unit.Runner.Labels[0] = "ambient"
			unit.Accepted.Spec.Hash = "changed-by-binding"
			unit.Accepted.Plan[0] = '['
		}
		cfg := nodehost.NativeHostConfig{
			Store: finishingStoreW9{NativeHostStore: binding.Tenant, finished: &finishedThroughServices},
			Lease: lease.Keeper{Lease: time.Minute, Interval: time.Second}, Source: source.Options{Engines: nativeStoreEnginesW8{}},
			Masks: nodehost.NewMaskRegistry(secrets.NewMasker()), Router: router,
			Processes:   &nodehost.NativeProcesses{Authorities: authorities, Log: func(context.Context, string, string, nodeproto.FrameLog) error { return nil }},
			PlanningLog: func(context.Context, nodeproto.FrameLog) error { return nil },
			Executor: nodehost.ExecutionOperations{Bind: func(context.Context, nodehost.NodeInput) (nodehost.ProcessGrant, error) {
				return nodehost.ProcessGrant{}, errors.New("unexpected work")
			}},
			ReadOutput: func(context.Context, runspec.NodeRef, int64) (json.RawMessage, error) {
				return nil, errors.New("unexpected output read")
			},
			Evaluation: nodehost.EvaluationOperations{
				Bind: func(context.Context, nodehost.EvaluationInput) (nodehost.ProcessGrant, error) {
					return nodehost.ProcessGrant{}, errors.New("unexpected evaluation")
				},
			},
			Owner: nodehost.OwnerOperations{
				Renew: func(ctx context.Context, actor runstate.Actor, computing bool) (runstate.LeaseBeat, error) {
					beat, err := s.AsOperator().RenewUnit(ctx, actor.CapabilityPrefix, computing)
					if mode == "remote_renewal" && err == nil && renewals.Add(1) == 2 {
						close(renewed)
					}
					return beat, err
				},
				Wait: func(context.Context, store.RunSnapshot) error { return errors.New("unexpected state wait") },
				Acquire: func(ctx context.Context, input nodehost.RunInput) (*source.Entry, error) {
					acquired = true
					if mode == "remote_renewal" {
						select {
						case <-renewed:
						case <-ctx.Done():
							return nil, context.Cause(ctx)
						}
					} else if mode == "remote_cancel" {
						cancel()
						<-ctx.Done()
						return nil, context.Cause(ctx)
					}
					if direct && !reflect.DeepEqual(input.GrantedRunner, expected) || input.GrantedRunner.Name != "runner" || input.Owner.Authority != binding.Principal.Authority || input.Owner.Owner.Generation != runspec.ClaimGeneration(binding.Principal.Capability.Binding.Generation) {
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
		if mode == "remote_renewal" {
			cfg.Lease.Interval = time.Millisecond
		}
		return nodehost.NativeServices{Config: cfg, Close: func() error { closed = true; close(done); return authorities.Close() }}, nil
	}
	var executionErr error
	remote := runners.Hosting{ControllerURL: "https://controller.example", NewController: cloudStoreClientW8(t, server), Bind: func(ctx context.Context, unit controllerwire.ClaimedUnit, _ controllerwire.Controller) (nodehost.NativeServices, capability.Principal, error) {
		principal, err := s.AsOperator().LookupCapability(ctx, sha256.Sum256([]byte(unit.Capability)))
		if err != nil {
			return nodehost.NativeServices{}, principal, err
		}
		tenant, err := s.ForTeam(ctx, store.Team(principal.Capability.Team))
		if err != nil {
			return nodehost.NativeServices{}, principal, err
		}
		services, err := bind(ctx, localcontroller.StoreBinding{Tenant: tenant, Principal: principal})
		services.Config.Store = tenant
		return services, principal, err
	}}
	if mode == "remote_award" || mode == "remote_renewal" || mode == "remote_cancel" {
		executionErr = remote.Host(ctx, unit)
	} else if mode == "managed_remote" {
		repositories, err := sourceurl.ParseRepoAllowlist([]string{"github.com/team/repo"})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := runners.NewManagedRunner(ctx, runners.RuntimeConfig{
			Controller: client.EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return root.Raw, nil }}, NewController: cloudStoreClientW8(t, server),
			Loop: runners.LoopConfig{Version: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitTrigger}, MaxConcurrent: 1, MaxClaims: 1}, Hosting: remote,
			Repositories: repositories,
			Versions:     runnerVersionPolicyW8(t, "v0.71.0"), Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner},
		})
		if err != nil {
			t.Fatal(err)
		}
		executionErr = runner.Run(ctx)
	} else if direct {
		executionErr = (localcontroller.StoreHosting{Controller: controller.NewEngineController(s, controller.EngineController{}), Bind: bind}).Host(ctx, unit)
	} else if mode == "embedded_queue" {
		runner, err := localcontroller.NewStoreRunner(server.EngineController, localcontroller.StoreRunnerConfig{
			Bearer: func(context.Context) (string, error) { return root.Raw, nil },
			Loop:   runners.LoopConfig{Version: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitTrigger}, MaxConcurrent: 1, MaxClaims: 1},
			Bind:   bind,
		})
		if err != nil {
			t.Fatal(err)
		}
		executionErr = runner.Run(ctx)
	} else {
		cfg := localcontroller.Config{Bind: func(_ context.Context, binding localcontroller.Binding) (localcontroller.Services, error) {
			s = binding.Store
			teststore.RegisterSQLite(s, filepath.Join(binding.Home, "state.db"))
			server, root = seedStoredRunnerW8(t, s, capability.UnitTrigger)
			prepareNativeStoreW8(t, s, capability.UnitTrigger)
			binding.Controller.RunnerPolicy = server.RunnerPolicy
			return localcontroller.Services{
				Policies: store.Policies{ClaimProfiles: runnerStorePolicyW8{}},
				State:    &nativeLocalStateW8{done: done, stop: cancel},
				HTTP:     localhostwire.LocalHTTP{API: http.NotFoundHandler(), Dashboard: http.NotFoundHandler()},
				Runner: localcontroller.StoreRunnerConfig{
					Bearer: func(context.Context) (string, error) { return root.Raw, nil },
					Loop:   runners.LoopConfig{Version: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitTrigger}, MaxConcurrent: 1},
					Bind:   bind,
				},
			}, nil
		}}
		owner, err := cfg.Open(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := owner.Close(); err != nil {
				t.Error(err)
			}
		}()
		if _, err := owner.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if err := owner.Ready(); err != nil {
			t.Fatal(err)
		}
		if terminal, err := owner.IsRunTerminal("run"); terminal || err != nil {
			t.Fatalf("owner store before execution: terminal=%t error=%v", terminal, err)
		}
		executionErr = owner.Run(ctx, func(wingd.LocalActivity) error { return nil })
		if executionErr != nil {
			requireLocalCancellationW8(t, executionErr)
			executionErr = nil
		}
		if terminal, err := owner.IsRunTerminal("run"); !terminal || err != nil {
			t.Fatalf("owner store after execution: terminal=%t error=%v", terminal, err)
		}
	}
	if executionErr != nil && !(mode == "remote_cancel" && errors.Is(executionErr, context.Canceled)) {
		t.Fatal(executionErr)
	}
	if (mode == "embedded_queue" || mode == "local_controller" || mode == "award_snapshot") && !finishedThroughServices {
		t.Fatal("embedded host discarded the service's pre-completion storage binding")
	}

	tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if mode == "remote_cancel" {
		if err != nil || !errors.Is(executionErr, context.Canceled) || run.Owner == nil || run.Owner.Holder != unit.Binding.Holder || run.Owner.Generation != runspec.ClaimGeneration(unit.Binding.Generation) || run.State == runstate.RunSuccess || run.State == runstate.RunFailed || run.State == runstate.RunCancelled || !bound || !acquired || !closed {
			t.Fatalf("cancelled acquisition finished or abandoned its claim: state=%s error=%v host error=%v", run.State, err, executionErr)
		}
		return
	}
	if err != nil || run.State != runstate.RunSuccess || !bound || !acquired || !closed {
		t.Fatalf("state=%s bound=%v acquired=%v closed=%v error=%v", run.State, bound, acquired, closed, err)
	}
}

func requireLocalCancellationW8(t *testing.T, err error) {
	t.Helper()
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, part := range joined.Unwrap() {
			requireLocalCancellationW8(t, part)
		}
	} else if wrapped := errors.Unwrap(err); wrapped != nil {
		requireLocalCancellationW8(t, wrapped)
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("local controller shutdown: %v", err)
	}
}

type nativeLocalStateW8 struct {
	done <-chan struct{}
	stop context.CancelFunc
}

func (*nativeLocalStateW8) Ready() error { return nil }

func (*nativeLocalStateW8) TypeSafeAPIKey(context.Context) (string, error) { return "", nil }

func (*nativeLocalStateW8) Reconcile(context.Context, store.SingletonLease) (wingd.LocalActivity, error) {
	return wingd.LocalActivity{Runs: 1, QueuedTriggers: 1}, nil
}

func (s *nativeLocalStateW8) Watch(ctx context.Context, report func(wingd.LocalActivity) error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		defer s.stop()
		return report(wingd.LocalActivity{})
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
			unit.Execution.Requires.Value = []string{"changed-label"}
		case 8:
			unit.Capability += value
		case 9:
			grants := []capability.Grant{capability.NodeReport}
			if delta&1 != 0 {
				grants = append(grants, capability.NodeResult)
			}
			unit.Capability = nativeStoreDelegateW8(t, s, unit.Capability, grants)
		}
		host := localcontroller.StoreHosting{Controller: controller.NewEngineController(s, controller.EngineController{}), Bind: func(context.Context, localcontroller.StoreBinding) (nodehost.NativeServices, error) {
			t.Fatal("changed award reached native service construction")
			return nodehost.NativeServices{}, nil
		}}
		if err := host.Host(t.Context(), unit); err == nil {
			t.Fatal("changed award was accepted")
		}
	})
}

func nativeStoreDelegateW8(t *testing.T, s *store.Store, raw string, grants []capability.Grant) string {
	t.Helper()
	principal, err := s.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	c := principal.Capability
	issued, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: principal.Authority, Audience: c.Audience, Grants: grants, Binding: c.Binding, Repositories: c.Repositories, ExpiresAt: c.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Raw
}

func TestNativeStoreRefusesDelegatedOwnerW8(t *testing.T) {
	s, unit := nativeStoreAwardW8(t, capability.UnitTrigger)
	unit.Capability = nativeStoreDelegateW8(t, s, unit.Capability, []capability.Grant{capability.TriggerHost})
	bound := false
	host := localcontroller.StoreHosting{Controller: controller.NewEngineController(s, controller.EngineController{}), Bind: func(context.Context, localcontroller.StoreBinding) (nodehost.NativeServices, error) {
		bound = true
		return nodehost.NativeServices{}, errors.New("delegated owner reached service construction")
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
			host := localcontroller.StoreHosting{Controller: controller.NewEngineController(s, controller.EngineController{}), Bind: func(context.Context, localcontroller.StoreBinding) (nodehost.NativeServices, error) {
				return nodehost.NativeServices{Close: func() error { closed++; return closeFailure }}, failure
			}}
			if err := host.Host(t.Context(), unit); !errors.Is(err, failure) || !errors.Is(err, closeFailure) || closed != 1 {
				t.Fatalf("partial native binding: closed=%d error=%v", closed, err)
			}
		})
	}
}

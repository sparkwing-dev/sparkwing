package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type storedRunnerW8 struct {
	controllerwire.Controller
	server     *controller.EngineController
	authorized capability.Authorized
}

func (c *storedRunnerW8) Claim(ctx context.Context, body controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return c.server.Claim(ctx, c.authorized, body)
}

func (c *storedRunnerW8) Renew(ctx context.Context, unit string, body controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	if unit != c.authorized.Principal.Capability.UnitID {
		return controllerwire.RenewalResponse{}, errors.New("renewal selected another unit")
	}
	return c.server.Renew(ctx, c.authorized, body)
}

func (c *storedRunnerW8) TransitionJob(ctx context.Context, run, job string, body controllerwire.JobTransitionRequest) (controllerwire.Applied, error) {
	authorized := c.authorized
	authorized.Target.RunID, authorized.Target.NodeID = run, job
	return c.server.TransitionJob(ctx, authorized, body)
}

func (c *storedRunnerW8) Finish(ctx context.Context, run string, body controllerwire.FinishRequest) (controllerwire.Applied, error) {
	authorized := c.authorized
	authorized.Target.RunID = run
	return c.server.Finish(ctx, authorized, body)
}

type runnerStorePolicyW8 struct{}

func (runnerStorePolicyW8) AuthorizeProvision(_ context.Context, _ capability.AuthorityResolver, spec capability.ProvisionSpec) error {
	if spec.RegistrationID != "runner-store-fixture" {
		return errors.New("unregistered fixture origin")
	}
	return nil
}

func (runnerStorePolicyW8) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	capacity := match.Resources{Cores: 2, MemoryBytes: 1 << 30}
	return match.Profile{Capacity: capacity}, capacity, nil
}

func (runnerStorePolicyW8) CheckRunner(_ context.Context, version string) error {
	if version != "v0.71.0" {
		return errors.New("unexpected runner version")
	}
	return nil
}

func newStoredRunnerW8(t *testing.T, kind capability.UnitKind) (*controller.EngineController, capability.Issued) {
	t.Helper()
	s, err := teststore.Open(filepath.Join(t.TempDir(), "runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	s.SetProvisionPolicy(runnerStorePolicyW8{})
	s.SetClaimProfileResolver(runnerStorePolicyW8{})
	operator, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, RegistrationID: "runner-store-fixture", Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens}})
	if err != nil {
		t.Fatal(err)
	}
	parent := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Capability.Prefix}
	root, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, RegistrationID: "runner-store-fixture", Authority: &parent, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}})
	if err != nil {
		t.Fatal(err)
	}
	unit := runnerAuthorityAwardW8(capability.UnitNode)
	unit.Request.Request.Team = string(store.DefaultTeam)
	unit.Request.Request.Git.Repository.Key = "gh:1"
	unit.Accepted.Spec.Request = unit.Request
	request, err := json.Marshal(unit.Request)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(unit.Accepted.Spec)
	if err != nil {
		t.Fatal(err)
	}
	plan := []byte(`{"jobs":[]}`)
	if kind == capability.UnitNode {
		plan = []byte(`{"jobs":[{"id":"job"}]}`)
	}
	now := time.Now().UTC()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days,spec_json,spec_hash,sealed_at,plan_json,plan_hash)
VALUES (?,'run',1,'fixture','running',?,'request',?,'gh:1','host','execute',?,'fixture',?,'r30',30,?,'spec',?,?,'plan')`, []any{store.DefaultTeam, request, now.UnixNano(), []byte(`[]`), now.UnixNano(), spec, now.UnixNano(), plan}},
		{`INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES (?,'run','trigger',?,?,?,?,?)`, []any{store.DefaultTeam, now.UnixNano(), now.UnixNano(), now.Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`)}},
	} {
		if _, err := s.DB().ExecContext(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if kind == capability.UnitNode {
		if _, err := s.DB().ExecContext(t.Context(), `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES (?,'run','job','pending',?,?,?)`, store.DefaultTeam, now.UnixNano(), []byte(`{"id":"job"}`), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	return &controller.EngineController{Store: s, RunnerPolicy: runnerStorePolicyW8{}}, root
}

func storedRunnerClientW8(ctx context.Context, server *controller.EngineController, bearer string) (*storedRunnerW8, error) {
	principal, err := server.Store.LookupCapability(ctx, sha256.Sum256([]byte(bearer)))
	if err != nil {
		return nil, err
	}
	c := principal.Capability
	return &storedRunnerW8{server: server, authorized: capability.Authorized{Principal: principal, Binding: c.Binding, Target: capability.Target{Team: c.Team, RunID: c.Binding.RunID, NodeID: c.Binding.NodeID}}}, nil
}

func TestRunnerStoreLifecycleW8Components(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			server, root := newStoredRunnerW8(t, kind)
			claimant, err := storedRunnerClientW8(t.Context(), server, root.Raw)
			if err != nil {
				t.Fatal(err)
			}
			if kind == capability.UnitNode {
				owner, err := claimant.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitTrigger}, AvailableCores: 2, AvailableMemoryBytes: 1 << 30})
				if err != nil || owner.Claim == nil {
					t.Fatalf("owner acquisition: present=%v error=%v", owner.Claim != nil, err)
				}
			}
			joined, closed, executed := false, false, false
			var held controllerwire.ClaimedUnit
			host := runners.Hosting{
				ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
				NewController: func(options client.EngineOptions) (controllerwire.Controller, error) {
					bearer, err := options.Bearer(t.Context())
					if err != nil {
						return nil, err
					}
					if bearer == root.Raw {
						t.Error("hosting reused machine root authority")
					}
					return storedRunnerClientW8(t.Context(), server, bearer)
				},
				StartKeeper: func(ctx context.Context, keeper lease.Keeper) (context.Context, func()) {
					if _, err := keeper.Renew(ctx); err != nil {
						t.Error(err)
					}
					return ctx, func() { joined = true }
				},
			}
			cleanup := func() error {
				if !joined {
					t.Error("source released before keeper joined")
				}
				closed = true
				return nil
			}
			host.PrepareRun = func(ctx context.Context, unit controllerwire.ClaimedUnit, bound controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
				held = unit
				return runnerRunW8(func() {
					executed = true
					if _, err := bound.Finish(ctx, unit.RunID, controllerwire.FinishRequest{Outcome: "success"}); err != nil {
						t.Error(err)
					}
				}), nodehost.RunInput{}, cleanup, nil
			}
			host.PrepareNode = func(ctx context.Context, unit controllerwire.ClaimedUnit, bound controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
				held = unit
				return identityNodeW8(func(input nodehost.NodeInput) {
					executed = true
					if input.Engine.Identity != *unit.Engine || input.GrantedRunner.Name != unit.Runner.Name || input.Generation != unit.Binding.Generation || input.Attempt != unit.Binding.Attempt {
						t.Error("host input lost its granted execution identity")
						return
					}
					started, err := bound.TransitionJob(ctx, unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "running", Cause: "start"})
					if err != nil || started.Attempt != 1 || unit.Binding.Attempt != 0 {
						t.Errorf("initial start: attempt=%d error=%v", started.Attempt, err)
						return
					}
					if _, err := bound.TransitionJob(ctx, unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "success", Cause: "report", Result: &controllerwire.JobResult{Outcome: "success"}}); err != nil {
						t.Error(err)
					}
				}), runnerIdentityInputW8(unit), cleanup, nil
			}
			loop := runners.Loop{Config: runners.LoopConfig{Version: "v0.71.0", MaxConcurrent: 1, MaxClaims: 1}, Controller: claimant, Host: host, CheckVersion: runnerStorePolicyW8{}.CheckRunner, RequestCapacity: func() (float64, int64) { return 2, 1 << 30 }}
			if err := loop.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !executed || !joined || !closed || held.Unit != kind || held.Runner.Name != "runner" {
				t.Fatalf("lifecycle: executed=%v joined=%v closed=%v kind=%s runner=%s", executed, joined, closed, held.Unit, held.Runner.Name)
			}
			tenant, err := server.Store.ForTeam(t.Context(), store.DefaultTeam)
			if err != nil {
				t.Fatal(err)
			}
			if kind == capability.UnitNode {
				row, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: held.RunID, NodeID: held.JobID})
				if err != nil || row.Outcome != store.NodeSuccess || row.Attempt != 1 || row.Generation != held.Binding.Generation || row.ClaimedBy != "" {
					t.Fatalf("persisted completion: outcome=%s attempt=%d generation=%d error=%v", row.Outcome, row.Attempt, row.Generation, err)
				}
			} else {
				row, err := tenant.ReadRun(t.Context(), held.RunID)
				if err != nil || row.State != store.RunSuccess {
					t.Fatalf("persisted completion: state=%s error=%v", row.State, err)
				}
			}
			bound, err := storedRunnerClientW8(t.Context(), server, held.Capability)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bound.Renew(t.Context(), held.UnitID, controllerwire.RenewalRequest{}); err == nil {
				t.Fatal("completed unit renewed")
			}
		})
	}
}

func TestRunnerStoreRevocationPreventsPreparationW8Components(t *testing.T) {
	server, root := newStoredRunnerW8(t, capability.UnitTrigger)
	claimant, err := storedRunnerClientW8(t.Context(), server, root.Raw)
	if err != nil {
		t.Fatal(err)
	}
	response, err := claimant.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitTrigger}, AvailableCores: 2, AvailableMemoryBytes: 1 << 30})
	if err != nil || response.Claim == nil {
		t.Fatalf("claim: present=%v error=%v", response.Claim != nil, err)
	}
	bound, err := storedRunnerClientW8(t.Context(), server, response.Claim.Capability)
	if err != nil {
		t.Fatal(err)
	}
	rootRef := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}
	tenant, err := server.Store.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: rootRef, CapabilityPrefix: rootRef.ID}, rootRef); err != nil {
		t.Fatal(err)
	}
	prepared, renewed := false, false
	host := runners.Hosting{
		ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
		NewController: func(client.EngineOptions) (controllerwire.Controller, error) { return bound, nil },
		StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) {
			renewed = true
			return ctx, func() {}
		},
		PrepareRun: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
			prepared = true
			return nil, nodehost.RunInput{}, nil, nil
		},
	}
	if err := host.Host(t.Context(), *response.Claim); err == nil || prepared || renewed {
		t.Fatalf("revoked root reached hosting: error=%v prepared=%v keeper=%v", err, prepared, renewed)
	}
}

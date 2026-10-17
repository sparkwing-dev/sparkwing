package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type hostIntakePolicy struct{}

func (hostIntakePolicy) ResolveIntake(ctx context.Context, resolver capability.AuthorityResolver, request store.IntakeRequest) (store.IntakePolicyFacts, error) {
	_, err := resolver.ResolveAuthority(ctx, request.Authority)
	return store.IntakePolicyFacts{Options: &request.Request.Options, SourceLocation: &request.Request.Source.Location, RepositoryAuthorized: true, LogRetentionClass: "r30", LogRetentionDays: 30}, err
}

type hostEnrollment struct{}

func (hostEnrollment) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	capacity := match.Resources{Cores: 64, MemoryBytes: 64 << 30}
	return match.Profile{Capacity: capacity}, capacity, nil
}

type hostState struct {
	*store.Tenant
	mu            sync.Mutex
	subscriptions map[chan struct{}]bool
}

func (s *hostState) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for changed := range s.subscriptions {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
}

func (s *hostState) subscribe(context.Context, string) (StateSubscription, error) {
	changed := make(chan struct{}, 1)
	s.mu.Lock()
	if s.subscriptions == nil {
		s.subscriptions = map[chan struct{}]bool{}
	}
	s.subscriptions[changed] = true
	s.mu.Unlock()
	return StateSubscription{Changed: changed, Close: func() error {
		s.mu.Lock()
		delete(s.subscriptions, changed)
		s.mu.Unlock()
		return nil
	}}, nil
}

func (s *hostState) TransitionNode(ctx context.Context, change store.NodeTransition) (store.Applied, error) {
	applied, err := s.Tenant.TransitionNode(ctx, change)
	if err == nil {
		s.notify()
	}
	return applied, err
}

func (s *hostState) AcceptExpansion(ctx context.Context, expansion store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	accepted, err := s.Tenant.AcceptExpansion(ctx, expansion)
	if err == nil {
		s.notify()
	}
	return accepted, err
}

type storeHostFixture struct {
	OwnerBearer []byte
	Store       *store.Store
	Tenant      *store.Tenant
	State       *hostState
	Actor       store.Actor
	Control     store.Actor
	Accepted    AcceptedRun
	Input       RunInput
	Host        OwnerHost
}

func newStoreHostFixture(t *testing.T, plan nodeproto.PlanDoc) *storeHostFixture {
	t.Helper()
	return newStoreHostFixtureOn(t, openStore(t), plan, runspec.Options{})
}

func newStoreHostFixtureOn(t *testing.T, database *store.Store, plan nodeproto.PlanDoc, options runspec.Options) *storeHostFixture {
	t.Helper()
	request := runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}, Options: options}
	return newStoreHostFixtureForRequest(t, database, plan, request)
}

func newStoreHostFixtureForRequest(t *testing.T, database *store.Store, plan nodeproto.PlanDoc, request runspec.Request) *storeHostFixture {
	t.Helper()
	f := newStoreOwnerFixture(t, database, request)
	ctx, tenant, actor := t.Context(), f.Tenant, f.Actor
	admitted, err := tenant.ReadRun(ctx, f.Input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	selected := engine.Identity{Version: "v1.0.0", ManifestDigest: "manifest", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}
	accepted, err := tenant.AcceptPlan(ctx, store.PlanAcceptance{RunID: f.Input.RunID, Actor: actor, Seal: runspec.SealInput{Request: admitted.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Engine: selected, Args: map[string]string{}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionRun(ctx, store.RunTransition{RunID: f.Input.RunID, Actor: actor, To: store.RunRunning, Cause: store.RunStart}); err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(accepted.Spec)
	f.Accepted = AcceptedRun{RunID: f.Input.RunID, Spec: spec, SpecHash: string(accepted.Spec.Hash), Plan: plan, PlanHash: accepted.PlanHash}
	f.Input.Accepted = &f.Accepted
	f.Host.Acquire = func(context.Context, RunInput) (*source.Entry, error) {
		return &source.Entry{Engine: engine.Resolved{Identity: selected}}, nil
	}
	return f
}

func newStoreOwnerFixture(t *testing.T, database *store.Store, request runspec.Request) *storeHostFixture {
	t.Helper()
	ctx := t.Context()
	database.SetIntakePolicy(hostIntakePolicy{})
	database.SetClaimProfileResolver(hostEnrollment{})
	tenant, err := database.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	root := "host-fixture-root"
	if _, err := database.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root}); err != nil {
		now, until := time.Now(), time.Now().Add(time.Hour)
		credential := capability.Capability{Prefix: root, Digest: sha256.Sum256([]byte(root)), Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "host"}, Audience: capability.Controller, Grants: []capability.Grant{capability.RunsWrite, capability.TriggersClaim, capability.NodesClaim, capability.ApprovalsWrite, capability.RunsControl}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, IssuedAt: now, ExpiresAt: &until, Repositories: nil, ReservedPools: []string{}}
		record := store.CapabilityRecord{Version: 1, Value: credential}
		if err := record.Validate(); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(record)
		grants, _ := json.Marshal(credential.Grants)
		_, err = database.DB().ExecContext(ctx, `INSERT INTO capabilities(prefix,digest,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,created_at,created_by,expires_at,repo_scope) VALUES (?,?,?,'machine','host','controller',?,'delegate',1,?,'none','[]',?,'fixture',?,'null')`, root, hex.EncodeToString(credential.Digest[:]), store.DefaultTeam, grants, body, now.UnixNano(), until.UnixNano())
		if err != nil {
			t.Fatal(err)
		}
	}
	authority := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root}
	if request.Burst != nil {
		_, requester, err := teststore.ProvisionCredential(ctx, database, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "burst-requester"}, Grants: []capability.Grant{capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}})
		if err != nil {
			t.Fatal(err)
		}
		authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: requester.Prefix}
	}
	admitted, err := tenant.AdmitRun(ctx, store.IntakeRequest{Authority: authority, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: root, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: admitted.RunID}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
	requestBody, err := json.Marshal(admitted.Request.Request)
	if err != nil {
		t.Fatal(err)
	}
	f := &storeHostFixture{OwnerBearer: claim.Bearer, Store: database, Tenant: tenant, Actor: actor, Control: store.Actor{Kind: store.ActorOperator, Authority: authority, CapabilityPrefix: root}}
	f.Input = RunInput{RunID: admitted.RunID, Owner: actor, SealedRequest: requestBody, RequestHash: string(admitted.Request.Hash), GrantedRunner: nodeproto.RunnerIdentity{Name: "host", Type: "agent", Labels: []string{}}}

	f.State = &hostState{Tenant: tenant}
	f.Host = OwnerHost{Store: f.State, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour}, Mask: func(s string) string { return s }, Renew: func(ctx context.Context, actor store.Actor, executing bool) (store.LeaseBeat, error) {
		return database.RenewUnit(ctx, actor.CapabilityPrefix, executing)
	}, Local: func(store.NodeRow) bool { return true }, AdoptClaim: func(context.Context, store.ReconciledNode) error { return nil }, Layer: func(context.Context, LayerSpec, EvaluationInput, store.NodeRow, StorageBinding) (OwnerLayerResult, error) {
		return OwnerLayerResult{}, nil
	}}
	follower := StateFollower{Store: f.State, Subscribe: f.State.subscribe}
	f.Host.Wait, f.Host.WaitNode = follower.Wait, follower.WaitNode
	f.Host.Delegator = database
	return f
}

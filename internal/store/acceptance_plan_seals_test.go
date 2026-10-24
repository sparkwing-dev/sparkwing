package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func checkStorePlanSeals(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen(store.WithPolicies(sealStorePolicies))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	tenant, request, err := storePlanSealRequest(ctx, s)
	if err != nil {
		return err
	}
	if err := checkRejectedStorePlans(ctx, s, tenant, request); err != nil {
		return err
	}
	accepted, err := tenant.AcceptPlan(ctx, request)
	if err != nil || accepted.Replayed {
		return fmt.Errorf("first acceptance replayed=%t: %v", accepted.Replayed, err)
	}
	before, err := tenant.ReadSnapshot(ctx, request.RunID)
	if err != nil || before.Run.Spec == nil || len(before.Nodes) != 2 || before.Run.Spec.Hash != accepted.Spec.Hash || before.Run.Request.Hash != request.Seal.Request.Hash {
		return fmt.Errorf("accepted snapshot: %+v, %v", before, err)
	}
	var events int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		return err
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := target.TryOpen(store.WithPolicies(sealStorePolicies))
	if err != nil {
		return err
	}
	s = reopened
	tenant, err = s.ForTeam(ctx, acceptanceTeam)
	if err != nil {
		return err
	}
	replayed, err := tenant.AcceptPlan(ctx, request)
	if err != nil || !replayed.Replayed || replayed.Spec.Hash != accepted.Spec.Hash {
		return fmt.Errorf("acceptance after reopen: %+v, %v", replayed, err)
	}
	request.Seal.Resolved.Args = map[string]string{"message": "changed"}
	var drift *apierr.Error
	if _, err := tenant.AcceptPlan(ctx, request); !errors.As(err, &drift) || drift.Code != apierr.SpecDrift {
		return fmt.Errorf("changed acceptance: %v", err)
	}
	after, err := tenant.ReadSnapshot(ctx, request.RunID)
	before.StoreNow = after.StoreNow
	if err != nil || !reflect.DeepEqual(before, after) {
		return fmt.Errorf("replay or drift changed committed state: %v", err)
	}
	var afterEvents int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&afterEvents); err != nil {
		return err
	}
	if afterEvents != events {
		return fmt.Errorf("replay or drift added events: %d -> %d", events, afterEvents)
	}
	return nil
}

func storePlanSealRequest(ctx context.Context, s *store.Store) (*store.Tenant, store.PlanAcceptance, error) {
	return storePlanSealRequestWith(ctx, s, nil)
}

func storePlanSealRequestWith(ctx context.Context, s *store.Store, configure func(*store.IntakeRequest, *nodeproto.PlanDoc)) (*store.Tenant, store.PlanAcceptance, error) {
	return storePlanSealRequestFor(ctx, s, string(acceptanceTeam), false, configure)
}

func storePlanSealRequestFor(ctx context.Context, s *store.Store, identity string, metered bool, configure func(*store.IntakeRequest, *nodeproto.PlanDoc)) (*store.Tenant, store.PlanAcceptance, error) {
	team := store.Team(identity)
	member, err := s.AsOperator().ResolveSignIn(ctx, googleProfile(identity, identity+"@example.test"), store.SignUpConditions{})
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	if member.PersonalTeam != team {
		return nil, store.PlanAcceptance{}, fmt.Errorf("planner team: %s", member.PersonalTeam)
	}
	raw, _, _, err := s.AsOperator().CreateAccountSession(ctx, member.Account, team, time.Hour)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	digest := sha256.Sum256([]byte(raw))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	provisionAuthority := authority
	if metered {
		operator, err := acceptanceBootstrap(ctx, s)
		if err != nil {
			return nil, store.PlanAcceptance{}, err
		}
		provisionAuthority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Capability.Prefix}
	}
	issued, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, Authority: &provisionAuthority, Team: string(team), Metered: metered,
		RegistrationID: "runner:" + identity + ":host", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "host"},
		Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"},
		Grants: []capability.Grant{capability.TriggersClaim},
	})
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	parent := issued.Capability
	tenant, err := s.ForTeam(ctx, team)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	input := store.IntakeRequest{Authority: authority, Request: runspec.Request{Version: 1, Team: string(team), Pipeline: "fixture", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}, SHA: "0123456789abcdef0123456789abcdef01234567"}, Source: runspec.Source{Ref: "0123456789abcdef0123456789abcdef01234567"}, RequestedArgs: map[string]string{"message": "sealed"}, Options: runspec.Options{OwnerExecutionTimeout: runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "owner_execution_timeout"}}}}}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "alpha", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "beta", Needs: []string{"alpha"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
	if configure != nil {
		configure(&input, &plan)
	}
	admitted, err := tenant.AdmitRun(ctx, input)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	claim, err := s.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: parent.Prefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(admitted.RunID)}})
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan})
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	return tenant, store.PlanAcceptance{RunID: admitted.RunID, Actor: runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}, Seal: runspec.SealInput{Request: admitted.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Args: input.Request.RequestedArgs, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}}, nil
}

func checkRejectedStorePlans(ctx context.Context, s *store.Store, tenant *store.Tenant, request store.PlanAcceptance) error {
	for _, invalid := range []json.RawMessage{[]byte(`{"nodes":[]}`), []byte(`{"jobs":[{"id":"alpha","id":"beta"}]}`), []byte(`{"jobs":[{"id":"alpha","deps":["missing"]}]}`)} {
		bad := request
		bad.Seal.Plan = invalid
		if _, err := tenant.AcceptPlan(ctx, bad); err == nil {
			return errors.New("invalid declarations accepted")
		}
		before, err := tenant.ReadSnapshot(ctx, request.RunID)
		if err != nil || before.Run.Spec != nil || len(before.Nodes) != 0 {
			return fmt.Errorf("refused plan left partial state: %+v, %v", before, err)
		}
	}
	if _, err := s.DB().ExecContext(ctx, `CREATE TRIGGER refuse_second_job BEFORE INSERT ON nodes WHEN NEW.node_id='beta' BEGIN SELECT RAISE(ABORT,'refuse second job'); END`); err != nil {
		return err
	}
	if _, err := tenant.AcceptPlan(ctx, request); err == nil {
		return errors.New("failed insertion committed a plan")
	}
	snapshot, err := tenant.ReadSnapshot(ctx, request.RunID)
	if err != nil || snapshot.Run.Spec != nil || len(snapshot.Nodes) != 0 {
		return fmt.Errorf("failed insertion left a seal or jobs: %v", err)
	}
	_, err = s.DB().ExecContext(ctx, `DROP TRIGGER refuse_second_job`)
	return err
}

func TestStorePlanSeals(t *testing.T) {
	if err := checkStorePlanSeals(t); err != nil {
		t.Fatal(err)
	}
}

const acceptanceTeam store.Team = "planner"

type acceptanceClaimProfile struct{}

func (acceptanceClaimProfile) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, error) {
	return match.Profile{}, nil
}

func acceptanceAttempt[T ~int | ~int64](attempt T) *runspec.AttemptOrdinal {
	ordinal := runspec.AttemptOrdinal(attempt)
	return &ordinal
}

func TestStorePlanInsertionLimitUsesCanonicalRefusal(t *testing.T) {
	s := storetest.Open(t, store.WithPolicies(sealStorePolicies))
	tenant, request, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{"max_global_nodes_per_run": 1}); err != nil {
		t.Fatal(err)
	}
	_, err = tenant.AcceptPlan(t.Context(), request)
	var canonical *apierr.Error
	if !errors.Is(err, store.ErrComputeLimit) || !errors.As(err, &canonical) || canonical.Code != apierr.PlanInvalid {
		t.Fatalf("plan ceiling refusal: %v", err)
	}
	before, err := tenant.ReadSnapshot(t.Context(), request.RunID)
	if err != nil || before.Run.Spec != nil || len(before.Nodes) != 0 {
		t.Fatalf("failed acceptance left partial state: %+v %v", before, err)
	}
	if _, err := s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{"max_global_nodes_per_run": 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{"max_global_nodes_per_run": 1}); err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil || !replay.Replayed {
		t.Fatalf("accepted replay met the lowered ceiling: %+v %v", replay, err)
	}
}

func sealStorePolicies(*store.Store) (store.Policies, error) {
	return store.Policies{ClaimProfiles: acceptanceClaimProfile{}}, nil
}

func fixturePolicies(policies store.Policies) store.Option {
	return store.WithPolicies(func(*store.Store) (store.Policies, error) { return policies, nil })
}

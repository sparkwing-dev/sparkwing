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
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

type sealIntakePolicy struct{}

func (sealIntakePolicy) ResolveIntake(ctx context.Context, reader capability.AuthorityResolver, request store.IntakeRequest) (store.IntakePolicyFacts, error) {
	_, err := reader.ResolveAuthority(ctx, request.Authority)
	return store.IntakePolicyFacts{Options: &request.Request.Options, LogRetentionClass: "r30", LogRetentionDays: 30}, err
}

func checkStorePlanSeals(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen()
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
	reopened, err := target.TryOpen()
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
	s.SetIntakePolicy(sealIntakePolicy{})
	s.SetClaimProfileResolver(acceptanceClaimProfile{})
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	s.SetSessionKeyCipher(cipher)
	member, err := s.ResolveSignIn(ctx, googleProfile("planner", "planner@example.test"), store.SignUpConditions{}, time.Now())
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	if member.PersonalTeam != acceptanceTeam {
		return nil, store.PlanAcceptance{}, fmt.Errorf("planner team: %s", member.PersonalTeam)
	}
	raw, _, _, err := s.CreateAccountSession(ctx, member.Account, acceptanceTeam, time.Hour)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	digest := sha256.Sum256([]byte(raw))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := s.ProvisionCapability(ctx, capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, Authority: &authority, Team: string(acceptanceTeam),
		RegistrationID: "runner:planner:host", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "host"},
		Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"},
		Grants: []capability.Grant{capability.TriggersClaim},
	})
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	parent := issued.Capability
	tenant, err := s.ForTeam(ctx, acceptanceTeam)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	input := store.IntakeRequest{Authority: authority, Request: runspec.Request{Version: 1, Team: string(acceptanceTeam), Pipeline: "fixture", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}, RequestedArgs: map[string]string{"message": "sealed"}, Options: runspec.Options{OwnerExecutionTimeout: runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "owner_execution_timeout"}}}}}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "alpha", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "beta", Needs: []string{"alpha"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
	if configure != nil {
		configure(&input, &plan)
	}
	admitted, err := tenant.AdmitRun(ctx, input)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	claim, err := s.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: parent.Prefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: admitted.RunID}, Available: match.Resources{Cores: 8}})
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
	return tenant, store.PlanAcceptance{RunID: admitted.RunID, Actor: store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}, Seal: runspec.SealInput{Request: admitted.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Args: input.Request.RequestedArgs, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}}, nil
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

func (acceptanceClaimProfile) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	capacity := match.Resources{Cores: 8}
	return match.Profile{Capacity: capacity}, capacity, nil
}

func acceptanceAttempt(attempt int64) *int64 { return &attempt }

func TestStorePlanInsertionLimitUsesCanonicalRefusal(t *testing.T) {
	s := storetest.Open(t)
	tenant, request, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{store.ComputeLimitGlobalNodesPerRun: 1}); err != nil {
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
	if _, err := s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{store.ComputeLimitGlobalNodesPerRun: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{store.ComputeLimitGlobalNodesPerRun: 1}); err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil || !replay.Replayed {
		t.Fatalf("accepted replay met the lowered ceiling: %+v %v", replay, err)
	}
}

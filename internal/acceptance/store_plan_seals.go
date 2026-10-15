package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type sealIntakePolicy struct{}

func (sealIntakePolicy) ResolveIntake(ctx context.Context, reader capability.AuthorityResolver, request store.IntakeRequest) (store.IntakePolicyFacts, error) {
	_, err := reader.ResolveAuthority(ctx, request.Authority)
	return store.IntakePolicyFacts{Options: &request.Request.Options, LogRetentionClass: "r30", LogRetentionDays: 30}, err
}

// CheckStorePlanSeals checks atomic acceptance and immutable replay through SQLite reopening.
func CheckStorePlanSeals(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "plan-seals.db")
	s, err := store.Open(path)
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
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	s = reopened
	tenant, err = s.ForTeam(ctx, store.DefaultTeam)
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
	s.SetIntakePolicy(sealIntakePolicy{})
	s.SetClaimProfileResolver(recoveryEnrollment{})
	until := time.Now().Add(time.Hour)
	parent := capability.Capability{Prefix: "planner-root", Digest: sha256.Sum256([]byte("planner-root")), Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "host"}, Audience: capability.Controller, Grants: []capability.Grant{capability.RunsWrite, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, IssuedAt: time.Now(), ExpiresAt: &until}
	if err := seedRecoveryCapability(ctx, s, parent); err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	input := store.IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Prefix}, Request: runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "fixture", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}, RequestedArgs: map[string]string{"message": "sealed"}, Options: runspec.Options{OwnerExecutionTimeout: runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "owner_execution_timeout"}}}}}
	admitted, err := tenant.AdmitRun(ctx, input)
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	claim, err := s.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: parent.Prefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: admitted.RunID}, Available: match.Resources{Cores: 8}})
	if err != nil {
		return nil, store.PlanAcceptance{}, err
	}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "alpha", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "beta", Needs: []string{"alpha"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
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

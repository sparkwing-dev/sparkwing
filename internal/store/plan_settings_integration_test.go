package store

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

type settingsPlanValidator struct{ sharedPlanContracts }

func (settingsPlanValidator) ValidatePlan(raw json.RawMessage, _ nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
	var plan nodeproto.PlanDoc
	err := json.Unmarshal(raw, &plan)
	return plan, err
}

func sharedSettingsPlan(t *testing.T, plan nodeproto.PlanDoc) (*Tenant, PlanAcceptance) {
	t.Helper()
	tenant, in, now := intakeFixture(t)
	tenant.s.intake = nil
	tenant.s.plans = settingsPlanValidator{}
	in.Request.Options.OwnerExecutionTimeout.Origin = runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "owner_execution_timeout"}
	in.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"message"}}
	admitted, err := tenant.AdmitRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	owner := authorityRoot("planner", "a")
	owner.Parent = &in.Authority
	owner.Issuance, owner.Unit = capability.Award, capability.UnitTrigger
	owner.Grants = []capability.Grant{capability.NodeResult}
	owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: admitted.RunID, Holder: "host", Generation: 1}
	persistPlanOwner(t, tenant.s, owner)
	seedTriggerClaim(t, tenant.s, admitted.RunID, "host", "planner", 1, now.Add(time.Hour))
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, PlanAcceptance{RunID: admitted.RunID, Actor: Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "planner"}, CapabilityPrefix: "planner", Owner: OwnerLeaseRef{Kind: OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(admitted.RunID), Holder: "host", Generation: 1}}}, Seal: runspec.SealInput{Request: admitted.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Args: in.Request.RequestedArgs, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}}
}

func TestAcceptanceUsesSharedSettingsAndSeals(t *testing.T) {
	cores, timeout := 1.5, int64(1234)
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Resources: &nodeproto.ResourceSpec{Cores: &cores}, TimeoutMS: &timeout, Requires: []string{"linux"}}}}
	tenant, request := sharedSettingsPlan(t, plan)
	accepted, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(accepted.Spec.Resolved.SecretArgs, []string{"message"}) {
		t.Fatalf("intake classification lost: %+v", accepted.Spec.Resolved.SecretArgs)
	}
	node, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	if node.Execution.Resources.Value.Cores.Value != cores || node.Execution.Timeout.Value != 1234*time.Millisecond || !slices.Equal(node.Execution.Requires.Value, []string{"linux"}) || node.Attempt != 0 {
		t.Fatalf("resolved execution: %+v, attempt %d", node.Execution, node.Attempt)
	}
	replayed, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Spec.Hash != accepted.Spec.Hash {
		t.Fatalf("shared seal replay: %+v, %v", replayed, err)
	}
}

func TestAcceptanceRerunsSharedSettingsBeforePersistence(t *testing.T) {
	cores := 1.5
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Resources: &nodeproto.ResourceSpec{Cores: &cores}}}}
	tenant, request := sharedSettingsPlan(t, plan)
	zero := float64(0)
	plan.Jobs[0].Resources.Cores = &zero
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	request.Seal.Plan = body
	if _, err := tenant.AcceptPlan(t.Context(), request); err == nil {
		t.Fatal("explicit zero accepted using the planner's previous winners")
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.Spec != nil {
		t.Fatalf("rejected seal persisted: %+v, %v", run.Spec, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
}

func TestAcceptancePersistsConcurrencyWinnersFromResolutionContract(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchHost)
	want := []runspec.Concurrency{{Group: "deploy", Scope: "global", Capacity: 1, Cost: 1, OnLimit: "queue"}}
	resolved := request.Seal.Resolved.Settings
	job := resolved.Jobs["job"]
	job.Concurrency = want
	resolved.Jobs["job"] = job
	request.Seal.Resolved.Settings = resolved
	tenant.s.intake = fakeIntakeContracts{resolve: func(settings.PlanInputs) (runspec.ExecutionSettings, error) { return resolved, nil }}
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"})
	if err != nil || !reflect.DeepEqual(row.Execution.Concurrency, want) {
		t.Fatalf("persisted concurrency winners: %+v, %v", row.Execution.Concurrency, err)
	}
}

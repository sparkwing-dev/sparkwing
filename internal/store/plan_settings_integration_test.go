package store

import (
	"encoding/json"
	"errors"
	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func sharedSettingsPlan(t *testing.T, plan nodewire.PlanDoc) (*Tenant, PlanAcceptance) {
	t.Helper()
	tenant, in, now := intakeFixture(t)
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
	body, err := json.Marshal(testBodyPlan(plan))
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, PlanAcceptance{RunID: admitted.RunID, Actor: runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "planner"}, CapabilityPrefix: "planner", Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(admitted.RunID), Holder: "host", Generation: 1}}}, Seal: runspec.SealInput{Request: admitted.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Args: in.Request.RequestedArgs, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}}
}

func TestAcceptanceUsesSharedSettingsAndSeals(t *testing.T) {
	timeout := int64(1234)
	plan := nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job", TimeoutMS: &timeout, Requires: []string{"linux"}}}}
	tenant, request := sharedSettingsPlan(t, plan)
	accepted, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(accepted.Spec.Resolved.SecretArgs, []string{"message"}) {
		t.Fatalf("intake classification lost: %+v", accepted.Spec.Resolved.SecretArgs)
	}
	node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	if node.Execution.Timeout.Value != 1234*time.Millisecond || !slices.Equal(node.Execution.Requires.Value, []string{"linux"}) || node.Attempt != 0 {
		t.Fatalf("resolved execution: %+v, attempt %d", node.Execution, node.Attempt)
	}
	replayed, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Spec.Hash != accepted.Spec.Hash {
		t.Fatalf("shared seal replay: %+v, %v", replayed, err)
	}
}

func TestAcceptanceRerunsSharedSettingsBeforePersistence(t *testing.T) {
	plan := nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job"}}}
	tenant, request := sharedSettingsPlan(t, plan)
	invalidTimeout := int64(-1)
	plan.Jobs[0].TimeoutMS = &invalidTimeout
	body, err := json.Marshal(testBodyPlan(plan))
	if err != nil {
		t.Fatal(err)
	}
	request.Seal.Plan = body
	if _, err := tenant.AcceptPlan(t.Context(), request); err == nil {
		t.Fatal("negative timeout accepted using the planner's previous winners")
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
	request.Seal.Plan = []byte(`{"jobs":[{"id":"job","steps":[{"id":"step"}],"concurrency":[{"group":"deploy","capacity":1,"cost":1,"scope":"global","on_limit":"queue"}]}]}`)
	var plan nodewire.PlanDoc
	if err := json.Unmarshal(request.Seal.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	request.Seal.Resolved.Settings, _ = settings.ResolvePlan(settings.PlanInputs{Request: request.Seal.Request, Declarations: request.Seal.Declarations, Plan: plan})
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"})
	if err != nil || !reflect.DeepEqual(row.Execution.Concurrency, want) {
		t.Fatalf("persisted concurrency winners: %+v, %v", row.Execution.Concurrency, err)
	}
}

func TestAcceptanceRejectsSecretEffectiveLabelsBeforePersistence(t *testing.T) {
	for _, empty := range []bool{false, true} {
		for _, layer := range []string{"defaults", "pipeline", "losing-defaults"} {
			for _, label := range []string{"requires", "prefers", "profile"} {
				if label == "profile" && layer != "pipeline" {
					continue
				}
				name := layer + "/" + label
				if empty {
					name += "/empty"
				}
				t.Run(name, func(t *testing.T) {
					plan := nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job"}}}
					if empty {
						plan.Jobs = []nodewire.JobSpec{}
					}
					tenant, request := sharedSettingsPlan(t, plan)
					value, safe := "[pool=hello]", "[linux]"
					if label == "profile" {
						value, safe = "hello", "safe"
					}
					config := "defaults: {" + label + ": " + value + "}"
					if layer == "pipeline" {
						config = "pipelines: [{name: p, entrypoint: p, " + label + ": " + value + "}]"
					} else if layer == "losing-defaults" {
						config += "\npipelines: [{name: p, entrypoint: p, " + label + ": " + safe + "}]"
					}
					declarations, err := settings.ProjectCheckoutDeclarations([]byte(config), ".sparkwing/sparkwing.yaml", "p")
					if err != nil {
						t.Fatal(err)
					}
					request.Seal.Declarations = declarations
					request.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Request: request.Seal.Request, Declarations: declarations, Plan: plan})
					if err != nil {
						t.Fatal(err)
					}
					_, err = tenant.AcceptPlan(t.Context(), request)
					var refusal *apierr.Error
					if !errors.As(err, &refusal) || refusal.Code != hosterr.SecretInIdentifier {
						t.Fatalf("effective secret label persisted instead of refused: %v", err)
					}
					if strings.Contains(err.Error(), "hello") {
						t.Fatal("refusal disclosed secret")
					}
					snapshot, err := tenant.ReadSnapshot(t.Context(), request.RunID)
					if err != nil {
						t.Fatal(err)
					}
					if snapshot.Run.Spec != nil || len(snapshot.Run.Plan) != 0 || len(snapshot.Nodes) != 0 {
						t.Fatal("refused effective identifiers persisted acceptance")
					}
				})
			}
		}
	}
}

// The fixture models a correctly sealed row written before identifier validation covered YAML.
func TestRetryRefusesPreviouslySealedSecretLabels(t *testing.T) {
	for _, field := range []string{"requires", "prefers", "profile", "resolved-profile"} {
		t.Run(field, func(t *testing.T) {
			tenant, request := sharedSettingsPlan(t, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job"}}})
			accepted, err := tenant.AcceptPlan(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			request.Seal.Resolved = accepted.Spec.Resolved
			config := "defaults: {" + field + ": [pool=hello]}\npipelines: [{name: p, entrypoint: p, " + field + ": [linux]}]"
			if field == "profile" {
				config = "pipelines: [{name: p, entrypoint: p, profile: hello}]"
			}
			if field == "resolved-profile" {
				config = ""
			}
			request.Seal.Declarations, err = settings.ProjectCheckoutDeclarations([]byte(config), ".sparkwing/sparkwing.yaml", "p")
			if err != nil {
				t.Fatal(err)
			}
			request.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Request: request.Seal.Request, Declarations: request.Seal.Declarations, Plan: nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job"}}}})
			if err != nil {
				t.Fatal(err)
			}
			if field == "resolved-profile" {
				request.Seal.Resolved.Settings.Options.Profile.Value = "hello"
				request.Seal.Resolved.Settings.Options.Profile.Set = true
			}
			tainted, err := runspec.SealSpec(request.Seal)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(tainted)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET spec_json=?,spec_hash=? WHERE team=? AND id=?`, raw, tainted.Hash, tenant.team, request.RunID)
			_, err = tenant.AdmitRun(t.Context(), IntakeRequest{Authority: request.Seal.Request.AuthorityRef, IdempotencyKey: "tainted-retry", Request: runspec.Request{Pipeline: "p", Lineage: runspec.Lineage{RetryOf: request.RunID}}})
			var refusal *apierr.Error
			if !errors.As(err, &refusal) || refusal.Code != hosterr.SecretInIdentifier {
				t.Fatalf("tainted source was not refused: %v", err)
			}
			if strings.Contains(err.Error(), "hello") {
				t.Fatal("retry refusal disclosed secret")
			}
			rewriteSchemaCount(t, tenant.s.db, "runs", 1)
			rewriteSchemaCount(t, tenant.s.db, "nodes", 1)
		})
	}
}

package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestPlanSealPreservesRawListInputsAcrossSubmissionSources(t *testing.T) {
	const raw = " prod, , dev "
	for _, origin := range []string{"api", "schedule", "child"} {
		t.Run(origin, func(t *testing.T) {
			var tenant *Tenant
			var admitted AdmissionResult
			var err error
			switch origin {
			case "schedule":
				var schedule CronSchedule
				var now time.Time
				tenant, schedule, now = scheduleIntakeFixture(t, 1)
				rewriteSchemaExec(t, tenant.s.db, `UPDATE cron_schedules SET args=?`, `{"targets":" prod, , dev "}`)
				schedule, err = tenant.GetCronSchedule(t.Context(), schedule.ID)
				if err == nil {
					admitted, err = tenant.AdmitScheduledRun(t.Context(), schedule, now)
				}
			default:
				var request IntakeRequest
				if origin == "child" {
					tenant, request = childIntakeFixture(t)
				} else {
					tenant, request, _ = intakeFixture(t)
				}
				request.Request.RequestedArgs = map[string]string{"targets": raw}
				admitted, err = tenant.AdmitRun(t.Context(), request)
			}
			if err != nil {
				t.Fatal(err)
			}
			parent := authorityRoot("arguments-parent", "a")
			authorityPersist(t, tenant.s, parent)
			owner := authorityRoot("arguments-planner", "a")
			owner.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Prefix}
			owner.Unit, owner.Grants = capability.UnitTrigger, []capability.Grant{capability.NodeResult}
			owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: admitted.RunID, Holder: "host", Generation: 1}
			if admitted.Request.Request.Dispatch == runspec.DispatchController {
				owner.Unit, owner.Binding.Kind = capability.UnitPlan, capability.BindingRun
			}
			persistPlanOwner(t, tenant.s, owner)
			seedTriggerClaim(t, tenant.s, admitted.RunID, "host", owner.Prefix, 1, tenant.s.now().Add(time.Hour))
			plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}
			declarations := runspec.CheckoutDeclarations{Version: 1}
			resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan})
			if err != nil {
				t.Fatal(err)
			}
			request := PlanAcceptance{RunID: admitted.RunID, Actor: runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: owner.Prefix}, CapabilityPrefix: owner.Prefix, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(admitted.RunID), Holder: "host", Generation: 1}}}, Seal: runspec.SealInput{Request: admitted.Request, Declarations: declarations, Plan: []byte(`{"jobs":[]}`), Resolved: runspec.Resolved{ArgumentDeclarations: []nodeproto.ArgumentDescription{{Name: "targets", Type: "[]string"}}, Args: map[string]string{"targets": "prod,dev"}, SecretArgs: []string{}, Settings: resolved}}}
			accepted, err := tenant.AcceptPlan(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if accepted.Spec.Request.Hash != admitted.Request.Hash || accepted.Spec.Request.Request.RequestedArgs["targets"] != raw || accepted.Spec.Resolved.Args["targets"] != "prod,dev" {
				t.Fatalf("raw identity or canonical arguments changed: %+v", accepted.Spec)
			}
			request.Seal.Resolved.Args = map[string]string{"targets": "dev,prod"}
			if _, err := tenant.AcceptPlan(t.Context(), request); err == nil {
				t.Fatal("changed canonical arguments accepted")
			}
		})
	}
}

func TestPlanSealResolvesCheckoutArgumentsBeforeDeclaredDefaults(t *testing.T) {
	for _, source := range []string{"required", "default", "intake"} {
		t.Run(source, func(t *testing.T) {
			tenant, request, _ := planFixtureWithInput(t, runspec.DispatchHost, func(in *IntakeRequest) {
				if source == "intake" {
					in.Request.RequestedArgs["targets"] = " prod, , dev "
				}
			})
			project := "defaults:\n  args: {targets: ' dev, , prod '}\n"
			if source != "required" {
				project += "pipelines:\n  - name: p\n    entrypoint: p\n    args: {targets: ' dev, , prod '}\n"
			}
			declarations, err := settings.ProjectCheckoutDeclarations([]byte(project), "sparkwing.yaml", "p")
			if err != nil {
				t.Fatal(err)
			}
			argument := nodeproto.ArgumentDescription{Name: "targets", Type: "[]string", Required: source == "required"}
			if source == "default" {
				value := "other"
				argument.Default = &value
			}
			request.Seal.Declarations, request.Seal.Resolved.ArgumentDeclarations = declarations, []nodeproto.ArgumentDescription{argument}
			request.Seal.Resolved.Args["targets"] = "dev,prod"
			if source == "intake" {
				request.Seal.Resolved.Args["targets"] = "prod,dev"
			}
			if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlanSealRejectsInvalidArgumentsAndChangedDefaults(t *testing.T) {
	for _, source := range []string{"required", "type", "enum", "declaration", "default", "injected"} {
		t.Run(source, func(t *testing.T) {
			tenant, request, _ := planFixture(t, runspec.DispatchHost)
			value := "expected"
			argument := nodeproto.ArgumentDescription{Name: "extra", Type: "string"}
			switch source {
			case "required":
				argument.Required = true
			case "type":
				argument.Name, argument.Type = "message", "int"
			case "enum":
				argument.Name, argument.Required, argument.Enum = "message", true, []string{"other"}
			case "declaration":
				argument.Required, argument.Default = true, &value
			case "default":
				argument.Default = &value
				request.Seal.Resolved.Args["extra"] = "changed"
			case "injected":
				request.Seal.Resolved.Args["extra"] = "injected"
			}
			request.Seal.Resolved.ArgumentDeclarations = []nodeproto.ArgumentDescription{argument}
			_, err := tenant.AcceptPlan(t.Context(), request)
			var canonical *apierr.Error
			want := apierr.InvalidArgs
			if source == "default" || source == "injected" {
				want = apierr.SpecDrift
			}
			if !errors.As(err, &canonical) || canonical.Code != want {
				t.Fatalf("invalid argument accepted without canonical refusal: %v", err)
			}
		})
	}
}

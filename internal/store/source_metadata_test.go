package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestSourceMetadataUsesOwnerLeaseWithoutWorkAttempt(t *testing.T) {
	tenant, _, request := expansionFixture(t)
	if err := tenant.WriteAnnotation(t.Context(), AnnotationWrite{Ref: request.Source, Actor: request.Actor, Message: "source note"}); err != nil {
		t.Fatal(err)
	}
	if err := tenant.WriteSummary(t.Context(), SummaryWrite{Ref: request.Source, Actor: request.Actor, Markdown: "source summary"}); err != nil {
		t.Fatal(err)
	}
	node, err := tenant.ReadNode(t.Context(), request.Source)
	if err != nil || node.Attempt != 3 || node.Generation != 2 {
		t.Fatalf("source identity changed: %+v %v", node, err)
	}
	if err := tenant.WriteSummary(t.Context(), SummaryWrite{Ref: request.Source, Actor: request.Actor, StepID: "attempt-step", Markdown: "wrong"}); err == nil {
		t.Fatal("source authority changed attempt metadata")
	}
}

func FuzzSourceMetadataCannotEscapeItsLease(f *testing.F) {
	for change := uint8(0); change < 9; change++ {
		f.Add(change, int64(1), false)
		f.Add(change, int64(1), true)
	}
	f.Fuzz(func(t *testing.T, change uint8, offset int64, evaluation bool) {
		tenant, now, request := expansionFixture(t)
		if evaluation {
			request = sourceMetadataEval(t, tenant, now, request)
		}
		if err := tenant.WriteSummary(t.Context(), SummaryWrite{Ref: request.Source, Actor: request.Actor, Markdown: "source"}); err != nil {
			t.Fatal(err)
		}
		switch change % 9 {
		case 0:
			request.Source.NodeID += "other"
		case 1:
			request.Source.RunID += "other"
		case 2:
			request.Actor.CapabilityPrefix += "other"
		case 3:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET generation=? WHERE team='a' AND run_id='run'`, 3+int64(uint64(offset)%1000))
		case 4:
			if evaluation {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE eval_units SET generation=9`)
				break
			}
			rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET generation=? WHERE team='a' AND id='run'`, 6+int64(uint64(offset)%1000))
		case 5:
			*now = now.Add(2 * time.Minute)
		case 6:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=1 WHERE team='a' AND id='run'`)
		case 7:
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET status='failed',finished_at=1 WHERE team='a' AND id='run'`)
		case 8:
			if evaluation {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET generation=5`)
			} else {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET claimed_by='other'`)
			}
		}
		if err := tenant.WriteSummary(t.Context(), SummaryWrite{Ref: request.Source, Actor: request.Actor, Markdown: "escaped"}); err == nil {
			t.Fatalf("changed source authority %d accepted", change%9)
		}
		if err := tenant.WriteAnnotation(t.Context(), AnnotationWrite{Ref: request.Source, Actor: request.Actor, Message: "escaped"}); err == nil {
			t.Fatalf("changed source authority %d accepted annotation", change%9)
		}
	})
}

func TestEvalMetadataKeepsSourceGenerationAndIndependentLease(t *testing.T) {
	for _, problem := range []string{"", "source", "eval lease", "controller lease", "foreign job"} {
		t.Run(problem, func(t *testing.T) {
			tenant, now, request := expansionFixture(t)
			request = sourceMetadataEval(t, tenant, now, request)
			switch problem {
			case "source":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET generation=3 WHERE run_id='run'`)
			case "eval lease":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE eval_units SET generation=9`)
			case "controller lease":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET generation=5`)
			case "foreign job":
				request.Source.NodeID += "foreign"
			}
			err := tenant.WriteSummary(t.Context(), SummaryWrite{Ref: request.Source, Actor: request.Actor, Markdown: "evaluation"})
			if (err == nil) != (problem == "") {
				t.Fatalf("eval metadata %q: %v", problem, err)
			}
		})
	}
}

func sourceMetadataEval(t *testing.T, tenant *Tenant, now *time.Time, request ExpansionSubmission) ExpansionSubmission {
	t.Helper()
	unitID, err := encodeAwardUnit(UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "job", SourceGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	eval := authorityRoot("eval-report", "a")
	eval.Unit, eval.UnitID, eval.Purpose = capability.UnitEval, unitID, capability.PurposeEval
	launcher := authorityRoot("eval-launcher", "a")
	launcher.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, tenant.s, launcher)
	eval.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: launcher.Prefix}
	eval.Issuance, eval.Delivery = capability.Award, capability.DeliveryDirect
	eval.Billing = &capability.BillingProvenance{PayerTeam: "a", ExecutorClass: "cloud", ReservationID: "eval-reservation", Start: capability.BillingAtAward, Unit: capability.BillingEval}
	eval.Grants = []capability.Grant{capability.ExpansionSubmit, capability.DataRead}
	eval.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 2, Expansion: &capability.LeaseRef{Kind: capability.UnitEval, ID: unitID, Holder: "eval-host", Generation: 8}}
	authorityPersist(t, tenant.s, eval)
	request.Actor.Authority.ID, request.Actor.CapabilityPrefix = eval.Prefix, eval.Prefix
	request.Actor.Owner = runstate.OwnerLeaseRef{Kind: runstate.OwnerEval, EvalFence: runspec.EvalFence{RunID: request.Source.RunID, SourceNodeID: request.Source.NodeID, SourceGeneration: request.SourceGeneration, Holder: "eval-host", Generation: 8}}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET dispatch='controller',plan_accepted_generation=1 WHERE id='run'`)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO leases(name,holder,generation,until) VALUES ('controller.owner','controller',4,?)`, now.Add(time.Minute).UnixNano())
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,generation,claimed_by,capability_prefix,lease_expires_at,owner_generation,claimed_at,started_at) VALUES ('a','run','job',2,'running',8,'eval-host','eval-report',?,4,1,1)`, now.Add(time.Minute).UnixNano())
	return request
}

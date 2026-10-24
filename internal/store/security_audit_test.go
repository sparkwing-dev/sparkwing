package store

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func securityAuditEvent(t *testing.T, s *Store, kind string) {
	t.Helper()
	var actor, attrs, subject string
	if err := s.db.QueryRow(`SELECT actor,attrs,subject_id FROM audit_events WHERE kind=?`, kind).Scan(&actor, &attrs, &subject); err != nil {
		t.Fatalf("missing %s audit: %v", kind, err)
	}
	if actor == "" || subject == "" || strings.Contains(attrs+subject+actor, "example.test") || strings.Contains(attrs, "private comment") {
		t.Fatalf("invalid or personal audit: actor=%s subject=%s attrs=%s", actor, subject, attrs)
	}
}

func TestSecurityMutationsRequireAudit(t *testing.T) {
	t.Run("approval", func(t *testing.T) {
		tenant, ref, _, _, change := approvalReaperFixture(t, "fail")
		change.Extra.Approval.Comment = "private comment person@example.test"
		rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER refuse_security_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
		if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
			t.Fatal("unaudited approval committed")
		}
		gate, err := tenant.GetApproval(t.Context(), string(ref.RunID), string(ref.NodeID))
		if err != nil || gate.ResolvedAt != nil {
			t.Fatalf("approval escaped rollback: %+v %v", gate, err)
		}
		rewriteSchemaExec(t, tenant.s.db, `DROP TRIGGER refuse_security_audit`)
		if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
			t.Fatal(err)
		}
		securityAuditEvent(t, tenant.s, "approval.decided")
		if result, err := tenant.TransitionNode(t.Context(), change); err != nil || !result.Replayed {
			t.Fatalf("approval replay: %+v %v", result, err)
		}
		rewriteSchemaCount(t, tenant.s.db, "audit_events WHERE kind='approval.decided'", 1)
	})
	t.Run("cancel", func(t *testing.T) {
		s, tenant, _, change := cancellationFixture(t, capability.UnitTrigger)
		change.Reason = "private comment person@example.test"
		rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_security_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
		if _, err := tenant.TransitionRun(t.Context(), change); err == nil {
			t.Fatal("unaudited cancellation committed")
		}
		row, err := tenant.ReadRun(t.Context(), "run")
		if err != nil || row.CancelRequestedAt != nil {
			t.Fatalf("cancellation escaped rollback: %+v %v", row, err)
		}
		rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_security_audit`)
		if _, err := tenant.TransitionRun(t.Context(), change); err != nil {
			t.Fatal(err)
		}
		securityAuditEvent(t, s, "run.cancel_requested")
	})
	t.Run("job_cancel", func(t *testing.T) {
		s, _ := unitClaimFixture(t)
		tenant := &Tenant{s: s, team: "a"}
		actor := authorityMember(t, s, "owner", RoleOwner)
		actor.Kind = runstate.ActorOperator
		change := nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: runstate.NodeCancelled, Cause: causeCancel, Actor: actor}
		rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_security_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
		if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
			t.Fatal("unaudited job cancellation committed")
		}
		row, err := tenant.ReadNode(t.Context(), change.Ref)
		if err != nil || row.CancelRequestedAt != nil || row.FinishedAt != nil {
			t.Fatalf("job cancellation escaped rollback: %+v %v", row, err)
		}
		rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_security_audit`)
		if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
			t.Fatal(err)
		}
		securityAuditEvent(t, s, "job.cancel_requested")
	})
	t.Run("invitation", func(t *testing.T) {
		s, a, _, _ := accountMutationFixture(t)
		invitation := accountMutationInvitation(t, s)
		rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_security_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
		if _, err := s.AsOperator().AcceptAccountInvitation(t.Context(), a, invitation.ID); err == nil {
			t.Fatal("unaudited invitation acceptance committed")
		}
		var membership int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE team=? AND account_id=?`, invitation.Team, a.Principal.Capability.Subject.ID).Scan(&membership); err != nil || membership != 0 {
			t.Fatalf("acceptance escaped rollback: members=%d %v", membership, err)
		}
		rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_security_audit`)
		if _, err := s.AsOperator().AcceptAccountInvitation(t.Context(), a, invitation.ID); err != nil {
			t.Fatal(err)
		}
		securityAuditEvent(t, s, "invitation.accepted")
	})
	for _, kind := range []capability.ProvisionKind{capability.ProvisionMember, capability.ProvisionMachine, capability.ProvisionService, capability.ProvisionBootstrap} {
		t.Run(string(kind), func(t *testing.T) {
			s, spec := provisionOrigin(t, kind)
			if kind == capability.ProvisionMember {
				owner := authorityMember(t, s, "owner", RoleOwner)
				spec = capability.ProvisionSpec{Kind: kind, Authority: &owner.Authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone}}
			}
			var before int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM capabilities`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_security_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			if issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err == nil || issued.Raw != "" {
				t.Fatal("unaudited credential issued")
			}
			rewriteSchemaCount(t, s.db, "capabilities", before)
			rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_security_audit`)
			issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			securityAuditEvent(t, s, "token.minted")
			if kind == capability.ProvisionMachine || kind == capability.ProvisionService {
				securityAuditEvent(t, s, "runner.enrolled")
			}
			actor := runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}}
			if spec.Authority != nil {
				actor.Authority = *spec.Authority
			}
			a := inventoryAuthorization(t, s, actor, spec.Team, issued.Capability.Prefix)
			rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_security_audit BEFORE INSERT ON audit_events WHEN NEW.kind='token.rotated' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			if _, err := s.rotateCapability(t.Context(), Team(spec.Team), a, time.Hour, nil); err == nil {
				t.Fatal("unaudited rotation committed")
			}
			rewriteSchemaCount(t, s.db, "capabilities", before+1)
			rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_security_audit`)
			if _, err := s.rotateCapability(t.Context(), Team(spec.Team), a, time.Hour, nil); err != nil {
				t.Fatal(err)
			}
			securityAuditEvent(t, s, "token.rotated")
		})
	}
}

func TestDelegatedTokenRequiresAudit(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	parent := inventoryMemberRoot(t, s, owner)
	spec := capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Capability.Prefix}, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_security_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if issued, err := s.AsOperator().DelegateCapability(t.Context(), spec); err == nil || issued.Raw != "" {
		t.Fatal("unaudited delegated token issued")
	}
	rewriteSchemaCount(t, s.db, "capabilities", 1)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_security_audit`)
	issued, err := s.AsOperator().DelegateCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := s.db.QueryRow(`SELECT actor FROM audit_events WHERE kind='token.minted' AND subject_id=?`, digestAudit(issued.Capability.Prefix).String()).Scan(&actor); err != nil || actor != "owner" {
		t.Fatalf("delegation audit actor=%s: %v", actor, err)
	}
}

func TestCapabilityRotationRequiresAudit(t *testing.T) {
	s, spec := machineEnrollment(t)
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	a := inventoryAuthorization(t, s, runstate.Actor{Authority: *spec.Authority}, spec.Team, issued.Capability.Prefix)
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_security_audit BEFORE INSERT ON audit_events WHEN NEW.kind='token.rotated' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if rotated, err := s.rotateCapability(t.Context(), Team(spec.Team), a, time.Hour, nil); err == nil || rotated.Issued.Raw != "" {
		t.Fatal("unaudited runner rotation committed")
	}
	rewriteSchemaCount(t, s.db, "capabilities", 1)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_security_audit`)
	rotated, err := s.rotateCapability(t.Context(), Team(spec.Team), a, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	if err := s.db.QueryRow(`SELECT json_extract(attrs,'$.previous_digest') FROM audit_events WHERE kind='token.rotated' AND subject_id=?`, digestAudit(rotated.Issued.Capability.Prefix).String()).Scan(&previous); err != nil || previous != digestAudit(issued.Capability.Prefix).String() {
		t.Fatalf("rotation audit previous=%s: %v", previous, err)
	}
}

func TestActionsEnrollmentRequiresAudit(t *testing.T) {
	s, spec := provisionOrigin(t, capability.ProvisionActions)
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_security_audit BEFORE INSERT ON audit_events WHEN NEW.kind='runner.enrolled' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err == nil || issued.Raw != "" {
		t.Fatal("unaudited Actions enrollment committed")
	}
	rewriteSchemaCount(t, s.db, "capabilities", 0)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_security_audit`)
	if _, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	securityAuditEvent(t, s, "runner.enrolled")
}

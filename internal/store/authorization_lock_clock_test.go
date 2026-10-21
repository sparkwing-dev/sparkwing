package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestInvitationExpiresWhileWaitingForSignUpGate(t *testing.T) {
	s, authorized, _, now := accountMutationFixture(t)
	team := Team("invited-team")
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)`, team)
	authorityMember(t, s, "inviter", RoleOwner, team)
	expires := now.Add(time.Minute)
	rewriteSchemaExec(t, s.db, `INSERT INTO invitations(id,team,email,role,invited_by,created_at,expires_at) VALUES ('waiting',?,'member@example.test','editor','inviter',1,?)`, team, expires.UnixNano())
	connection := recordStoreFences(t, s)
	waited := false
	connection.beforeLock = func(query string) {
		if strings.HasPrefix(query, "SELECT mode, source, reason, set_by, updated_at,") {
			waited = true
			*now = expires
		}
	}
	_, err := s.AsOperator().AcceptAccountInvitation(t.Context(), authorized, "waiting")
	if !waited || !errors.Is(err, ErrInvitationClosed) {
		t.Fatalf("waited=%v acceptance=%v", waited, err)
	}
	var members, accepted int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE team=? AND account_id=?`, team, authorized.Principal.Capability.Subject.ID).Scan(&members); err != nil || members != 0 {
		t.Fatalf("expired members=%d error=%v", members, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM invitations WHERE id='waiting' AND accepted_at IS NOT NULL`).Scan(&accepted); err != nil || accepted != 0 {
		t.Fatalf("expired acceptances=%d error=%v", accepted, err)
	}
}

func TestExpansionRechecksControllerExpiryAfterEvalFenceRead(t *testing.T) {
	tenant, now, request := expansionFixture(t)
	s := tenant.s
	unitID, err := encodeAwardUnit(UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "job", SourceGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	eval := authorityRoot("evaluation", "a")
	eval.Grants = []capability.Grant{capability.ExpansionSubmit}
	eval.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 2, Expansion: &capability.LeaseRef{Kind: capability.UnitEval, ID: unitID, Holder: "eval-host", Generation: 8}}
	authorityPersist(t, s, eval)
	request.Actor.Authority.ID, request.Actor.CapabilityPrefix = eval.Prefix, eval.Prefix
	request.Actor.Owner = OwnerLeaseRef{Kind: OwnerEval, EvalFence: runspec.EvalFence{RunID: request.Source.RunID, SourceNodeID: request.Source.NodeID, SourceGeneration: request.SourceGeneration, Holder: "eval-host", Generation: 8}}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller',plan_accepted_generation=1 WHERE id='run'`)
	expires := now.Add(time.Minute)
	rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,generation,until) VALUES ('controller.owner','controller',4,?)`, expires.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,generation,claimed_by,capability_prefix,lease_expires_at,owner_generation) VALUES ('a','run','job',2,'running',8,'eval-host','evaluation',?,4)`, now.Add(time.Hour).UnixNano())
	connection := recordStoreFences(t, s)
	s.pinnedClock = func() time.Time { return *now }
	reads := 0
	connection.beforeLock = func(query string) {
		if strings.HasPrefix(query, "SELECT status,COALESCE(claimed_by,''),capability_prefix,generation,lease_expires_at,owner_generation FROM eval_units") {
			reads++
			if reads == 2 {
				*now = expires
			}
		}
	}
	if _, err := tenant.AcceptExpansion(t.Context(), request); reads != 2 || err == nil {
		t.Fatalf("eval reads=%d acceptance=%v", reads, err)
	}
	var expansion string
	if err := s.db.QueryRow(`SELECT expansion FROM nodes WHERE run_id='run' AND node_id='job'`).Scan(&expansion); err != nil || expansion != "expected" {
		t.Fatalf("committed expansion=%s error=%v", expansion, err)
	}
}

func TestRenewalRechecksLeaseAfterBillingRead(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitNode, capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, claim, now := renewalFixture(t, kind)
			connection := recordStoreFences(t, s)
			s.dialect = DialectSQLite
			waited := false
			connection.beforeQuery = func(query string) error {
				if strings.HasPrefix(query, "SELECT capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,settled_at,stop_reason,drain_deadline_at FROM billing_awards") && !waited {
					waited = true
					*now = claim.Award.LeaseExpiresAt
				}
				return nil
			}
			if beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); !waited || err == nil {
				t.Fatalf("billing waited=%v renewal=%+v error=%v", waited, beat, err)
			}
			table, predicate := "triggers", `id='run'`
			if kind == capability.UnitNode {
				table, predicate = "nodes", `run_id='run'`
			}
			if kind == capability.UnitEval {
				table, predicate = "eval_units", `run_id='run'`
			}
			var expires int64
			if err := s.db.QueryRow(`SELECT lease_expires_at FROM ` + table + ` WHERE ` + predicate).Scan(&expires); err != nil || expires != claim.Award.LeaseExpiresAt.UnixNano() {
				t.Fatalf("expired lease changed to %d error=%v", expires, err)
			}
		})
	}
}

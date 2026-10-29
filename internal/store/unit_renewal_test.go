package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func renewalFixture(t *testing.T, kind capability.UnitKind) (*Store, UnitDemand, UnitClaim, *time.Time) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	fundUnitClaim(t, s)
	now := s.now()
	s.clock = func() time.Time { return now }
	demand.Target = &UnitRef{Kind: kind, RunID: "run"}
	if kind == capability.UnitNode {
		demand.Target.NodeID = "job"
	}
	if kind == capability.UnitPlan || kind == capability.UnitEval {
		root := authorityRoot("launcher", "")
		root.Subject.Kind = capability.SubjectService
		root.Grants = []capability.Grant{capability.ClaimsLaunch}
		authorityPersist(t, s, root)
		demand.CapabilityPrefix = root.Prefix
		rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
		rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
	}
	if kind == capability.UnitEval {
		demand.Target.NodeID, demand.Target.SourceGeneration = "job", 7
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected'`)
		rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES ('a','run','job',7,'pending')`)
	}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if kind == capability.UnitPlan || kind == capability.UnitEval {
		issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
		if err != nil {
			t.Fatal(err)
		}
		claim.Award.CapabilityPrefix = issued.Capability.Prefix
	}
	return s, demand, claim, &now
}

func TestStartedUnitsRegisterLogsBeforeOutput(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, claim, _ := renewalFixture(t, kind)
			tenant := &Tenant{s: s, team: claim.Team}
			if kind != capability.UnitNode {
				view, err := tenant.ReadRunView(t.Context(), claim.RunID)
				if err != nil || len(view.Logs) != 1 {
					t.Fatalf("owner award has no stream before its asynchronous heartbeat: %+v %v", view.Logs, err)
				}
			}
			if kind == capability.UnitNode {
				actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
				admitExecution(t, tenant, actor)
				if _, err := tenant.TransitionNode(t.Context(), StartNode{Ref: runspec.NodeRef{RunID: actor.RunID, NodeID: actor.NodeID}, Actor: actor, SourceAttempt: sourceAttempt(0)}); err != nil {
					t.Fatal(err)
				}
			} else {
				for range 2 {
					if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err != nil {
						t.Fatal(err)
					}
				}
			}
			view, err := tenant.ReadRunView(t.Context(), claim.RunID)
			if err != nil || len(view.Logs) != 1 {
				t.Fatalf("started unit has no unique log stream: %+v %v", view.Logs, err)
			}
			subject := view.Logs[0]
			if kind == capability.UnitNode {
				if subject.Attempt == nil || subject.Attempt.Attempt != 1 || subject.Attempt.Generation != int64(claim.Award.Generation) {
					t.Fatalf("attempt log identity lost: %+v", subject)
				}
			} else if subject.Unit == nil || string(subject.Unit.Kind) != string(kind) || subject.Unit.Generation != int64(claim.Award.Generation) || kind == capability.UnitEval && (subject.Unit.Source == nil || subject.Unit.Source.Generation != claim.SourceGeneration) {
				t.Fatalf("owner log identity lost: %+v", subject)
			}
		})
	}
}

func TestRenewUnitRecordsOwnerExecutionForRecovery(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, claim, now := renewalFixture(t, kind)
			query := `SELECT started_at FROM triggers WHERE id='run'`
			if kind == capability.UnitEval {
				query = `SELECT started_at FROM eval_units`
			}
			if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
				t.Fatal(err)
			}
			var started sql.NullInt64
			if err := s.db.QueryRow(query).Scan(&started); err != nil || started.Valid {
				t.Fatalf("idle renewal started unit: %v, %v", started, err)
			}
			*now = now.Add(time.Second)
			firstStart := now.UnixNano()
			var beat runstate.LeaseBeat
			for range 2 {
				var err error
				beat, err = s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
				if err != nil {
					t.Fatal(err)
				}
				*now = now.Add(time.Second)
			}
			if err := s.db.QueryRow(query).Scan(&started); err != nil || !started.Valid || started.Int64 != firstStart {
				t.Fatalf("execution start changed: %v, %v", started, err)
			}
			*now = beat.ExpiresAt
			reaped, err := s.AsOperator().ReapUnits(t.Context(), 10)
			if err != nil || len(reaped) != 1 || !reaped[0].Requeued || reaped[0].Disruptions != 1 || reaped[0].Generation != int64(claim.Award.Generation) {
				t.Fatalf("started unit loss: %+v, %v", reaped, err)
			}
			if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err == nil {
				t.Fatal("reaped owner renewed")
			}
		})
	}
}

func TestRenewUnitRefusesStaleAndRootAuthority(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.1s measured in the short class; the full suite runs it")
	}
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		for _, problem := range []string{"root", "revoked", "expired", "generation", "holder"} {
			t.Run(string(kind)+"/"+problem, func(t *testing.T) {
				s, demand, claim, now := renewalFixture(t, kind)
				table, predicate := "triggers", `id='run'`
				if kind == capability.UnitEval {
					table, predicate = "eval_units", `run_id='run'`
				}
				if kind == capability.UnitNode {
					table, predicate = "nodes", `run_id='run'`
				}
				prefix := claim.Award.CapabilityPrefix
				switch problem {
				case "root":
					prefix = demand.CapabilityPrefix
				case "revoked":
					team := Team("a")
					if kind == capability.UnitPlan || kind == capability.UnitEval {
						team = ""
					}
					revokeClaimRoot(t, s, demand.CapabilityPrefix, team)
				case "expired":
					*now = claim.Award.LeaseExpiresAt
				case "generation":
					rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET generation=generation+1 WHERE `+predicate)
				case "holder":
					rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET claimed_by='successor' WHERE `+predicate)
				}
				_, err := s.AsOperator().RenewUnit(t.Context(), prefix, false)
				if problem == "revoked" {
					var refused *apierr.Error
					if !errors.As(err, &refused) || refused.Code != apierr.Unauthenticated {
						t.Fatalf("revocation refusal = %v", err)
					}
				} else {
					var refused *ErrIllegalTransition
					if !errors.As(err, &refused) || refused.Reason != ReasonHolderRights {
						t.Fatalf("renewal refusal = %v", err)
					}
				}
				var lease int64
				if err := s.db.QueryRow(`SELECT lease_expires_at FROM ` + table + ` WHERE ` + predicate).Scan(&lease); err != nil || lease != claim.Award.LeaseExpiresAt.UnixNano() {
					t.Fatalf("refused renewal changed lease: %d, %v", lease, err)
				}
			})
		}
	}
}

func TestRenewUnitDeliversCancellationWithoutStarting(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, claim, now := renewalFixture(t, kind)
			rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=? WHERE id='run'`, now.UnixNano())
			beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
			if err != nil || !beat.CancelRequested || !beat.ExpiresAt.After(*now) {
				t.Fatalf("cancelled renewal: %+v, %v", beat, err)
			}
			query := `SELECT started_at FROM triggers WHERE id='run'`
			if kind == capability.UnitEval {
				query = `SELECT started_at FROM eval_units`
			}
			if kind == capability.UnitNode {
				query = `SELECT started_at FROM nodes`
			}
			var started sql.NullInt64
			if err := s.db.QueryRow(query).Scan(&started); err != nil || started.Valid {
				t.Fatalf("cancelled unit started: %v, %v", started, err)
			}
		})
	}
}

func TestRenewUnitSeesOwnerCancellation(t *testing.T) {
	s, _, claim, now := renewalFixture(t, capability.UnitTrigger)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET cancel_requested_at=? WHERE id='run'`, now.UnixNano())
	beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
	if err != nil || !beat.CancelRequested {
		t.Fatalf("owner cancellation: %+v, %v", beat, err)
	}
	var started sql.NullInt64
	if err := s.db.QueryRow(`SELECT started_at FROM triggers`).Scan(&started); err != nil || started.Valid {
		t.Fatalf("cancelled owner started: %v, %v", started, err)
	}
}

func TestRenewUnitNodeRequiresCommittedStart(t *testing.T) {
	s, _, claim, now := renewalFixture(t, capability.UnitNode)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err == nil {
		t.Fatal("renewal invented a job start")
	}
	tenant := &Tenant{s: s, team: claim.Team}
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
	admitExecution(t, tenant, actor)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID)}, To: runstate.NodeRunning, Cause: runstate.CauseStart, Actor: actor, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
	if err != nil || !beat.ExpiresAt.After(claim.Award.LeaseExpiresAt) {
		t.Fatalf("running job renewal: %+v, %v", beat, err)
	}
	rewriteSchemaCount(t, s.db, "node_attempts", 1)
}

func TestRenewUnitEvalChecksSourceGeneration(t *testing.T) {
	for _, problem := range []string{"source_generation", "source_outcome", "expansion_accepted"} {
		t.Run(problem, func(t *testing.T) {
			s, _, claim, _ := renewalFixture(t, capability.UnitEval)
			switch problem {
			case "source_generation":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+1`)
			case "source_outcome":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET outcome='failed'`)
			case "expansion_accepted":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET expansion='accepted'`)
			}
			if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err == nil {
				t.Fatal("ended evaluation renewed")
			}
			var started sql.NullInt64
			if err := s.db.QueryRow(`SELECT started_at FROM eval_units`).Scan(&started); err != nil || started.Valid {
				t.Fatalf("invalid evaluation started: %v, %v", started, err)
			}
		})
	}
}

func TestRenewUnitRequiresBootstrapExchange(t *testing.T) {
	s, issued := adapterBootstrap(t)
	if _, err := s.AsOperator().RenewUnit(t.Context(), issued.Capability.Prefix, false); err == nil {
		t.Fatal("bootstrap renewed its unit")
	}
	exchanged, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: issued.Capability.Prefix})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().RenewUnit(t.Context(), exchanged.Capability.Prefix, false); err != nil {
		t.Fatal(err)
	}
}

func revokeClaimRoot(t *testing.T, s *Store, prefix string, team Team) {
	t.Helper()
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
	var err error
	if team != "" {
		actor := authorityMember(t, s, "claim-revoker", RoleOwner)
		err = (&Tenant{s: s, team: team}).RevokeAuthority(t.Context(), actor, ref)
	} else {
		operator := authorityRoot("revoker", "")
		operator.Subject.Kind = capability.SubjectOperator
		operator.Grants = []capability.Grant{capability.OperatorTokens}
		authorityPersist(t, s, operator)
		err = s.AsOperator().RevokeAuthority(t.Context(), runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Prefix}}, ref)
	}
	if err != nil {
		t.Fatal(err)
	}
}

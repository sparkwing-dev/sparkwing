package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func reconcileFixture(t *testing.T) (*Tenant, *time.Time, Actor) {
	t.Helper()
	tenant, now, _ := liveHolderFixture(t)
	expiry := now.Add(time.Hour)
	owner := authorityRoot("owner", "a")
	owner.Subject.ID = "host"
	owner.Grants = []capability.Grant{capability.TriggerHost, capability.NodesClaim}
	owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "host", Generation: 1}
	owner.ExpiresAt = &expiry
	authorityPersist(t, tenant.s, owner)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels) VALUES ('a','run','claimed','trigger',1,1,'host','owner',1,?,?,?)`, expiry.UnixNano(), []byte(`[]`), []byte(`[]`))
	actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, CapabilityPrefix: "owner", Owner: OwnerLeaseRef{Kind: OwnerTrigger, RunID: "run", Holder: "host", Generation: 1}}
	return tenant, now, actor
}

func TestReconcileWaitsForHolderUntilReaperCommits(t *testing.T) {
	tenant, now, actor := reconcileFixture(t)
	request := ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: SourceNone}
	for _, expired := range []bool{false, true} {
		if expired {
			rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET lease_expires_at=? WHERE run_id='run'`, now.Add(-time.Nanosecond).UnixNano())
		}
		got, err := tenant.ReconcileNode(t.Context(), request)
		if err != nil || got.Kind != WaitHolder || got.Claim != nil || got.Node.Generation != 2 || got.Node.StateSeq != 0 {
			t.Fatalf("reconcile expired=%v: %+v, %v", expired, got, err)
		}
	}
	rewriteSchemaCount(t, tenant.s.db, "events", 0)
}

func TestReconcileKeepsTerminalWithoutSourceOrNewEvent(t *testing.T) {
	tenant, _, actor := reconcileFixture(t)
	holder := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claim"}, CapabilityPrefix: "claim", Holder: "runner", Generation: 2}
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: holder, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(3)}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: SourceResume})
		if err != nil || got.Kind != KeepTerminal || got.Node.Outcome != NodeSuccess || got.Claim != nil {
			t.Fatalf("terminal reconciliation: %+v, %v", got, err)
		}
	}
	rewriteSchemaCount(t, tenant.s.db, "events", 1)
}

func TestReconcileRechecksOwnerAndSourcePolicy(t *testing.T) {
	for _, problem := range []string{"team", "owner_expired", "owner_generation", "revoked", "kind", "source_none", "source_mismatch", "policy"} {
		t.Run(problem, func(t *testing.T) {
			tenant, now, actor := reconcileFixture(t)
			request := ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: SourceNone}
			switch problem {
			case "team":
				tenant.team = "b"
			case "owner_expired":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET lease_expires_at=?`, now.UnixNano())
			case "owner_generation":
				request.Actor.Owner.Generation++
			case "revoked":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='owner'`)
			case "kind":
				request.Actor.Kind = ActorHolder
			case "source_none":
				request.Source = &MaterializeSource{Kind: SourceMemo}
			case "source_mismatch":
				request.Policy, request.Source = SourceMemo, &MaterializeSource{Kind: SourceManualRetry}
			case "policy":
				request.Policy = "any"
			}
			if _, err := tenant.ReconcileNode(t.Context(), request); err == nil {
				t.Fatal("invalid reconciliation succeeded")
			}
			rewriteSchemaCount(t, tenant.s.db, "events", 0)
		})
	}
}

func TestReconcileRacingOwnersReclaimOneReapedJob(t *testing.T) {
	tenant, now, actor := reconcileFixture(t)
	tenant.s.claimProfiles = claimProfileFake{profile: match.Profile{Capacity: match.Resources{Cores: 8}}, available: match.Resources{Cores: 8}}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET plan_json=?,plan_hash='plan',spec_json=?,spec_hash='spec',sealed_at=1 WHERE id='run'`, []byte(`{"jobs":[{"id":"job"}]}`), []byte(`{}`))
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET lease_expires_at=? WHERE run_id='run'`, now.Add(-time.Nanosecond).UnixNano())
	reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), SingletonLease{Name: "controller.reaper", Holder: "replica", Generation: 1}, 10)
	if err != nil || len(reaped) != 1 || !reaped[0].Requeued {
		t.Fatalf("reap: %+v, %v", reaped, err)
	}
	var wg sync.WaitGroup
	results := make(chan ReconciledNode, 2)
	errors := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Go(func() {
			<-start
			got, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: SourceResume})
			results <- got
			errors <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var execute, wait int
	for got := range results {
		switch got.Kind {
		case Execute:
			execute++
			if got.Claim == nil || got.Claim.Generation != 3 || got.Claim.Holder != "host" || len(got.Bearer) == 0 {
				t.Fatalf("claim: %+v", got)
			}
		case WaitHolder:
			wait++
		default:
			t.Fatalf("unexpected reconciliation: %+v", got)
		}
	}
	if execute != 1 || wait != 1 {
		t.Fatalf("execute=%d wait=%d", execute, wait)
	}
	events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 10)
	if err != nil || len(events) != 3 || events[0].Kind != "job_requeued" || events[1].Kind != "job_ready" || events[2].Kind != "job_claimed" {
		t.Fatalf("events: %+v, %v", events, err)
	}
}

func TestReconcileDoesNotExecuteUnresolvedDependency(t *testing.T) {
	tenant, _, actor := reconcileFixture(t)
	tenant.s.claimProfiles = claimProfileFake{profile: match.Profile{Capacity: match.Resources{Cores: 8}}, available: match.Resources{Cores: 8}}
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM node_attempts`)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='pending',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,started_at=NULL,ready_at=NULL,spec_json=?`, []byte(`{"id":"job","needs":["dependency"]}`))
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','dependency','pending',?,?)`, []byte(`{"id":"dependency"}`), []byte(`{}`))
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET plan_json=?,plan_hash='plan',spec_json=?,spec_hash='spec',sealed_at=1 WHERE id='run'`, []byte(`{"jobs":[{"id":"dependency"},{"id":"job","needs":["dependency"]}]}`), []byte(`{}`))
	if _, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: SourceNone}); err == nil {
		t.Fatal("executed before dependency")
	}
	rewriteSchemaCount(t, tenant.s.db, "events", 0)
}

func TestReconcileSelfClaimRenewalKeepsOwnerFence(t *testing.T) {
	for _, problem := range []string{"live", "dispatched", "expired", "revoked"} {
		t.Run(problem, func(t *testing.T) {
			tenant, now, actor := reconcileFixture(t)
			tenant.s.claimProfiles = claimProfileFake{profile: match.Profile{Capacity: match.Resources{Cores: 8}}, available: match.Resources{Cores: 8}}
			rewriteSchemaExec(t, tenant.s.db, `DELETE FROM node_attempts`)
			rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='pending',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,started_at=NULL,ready_at=1`)
			got, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: SourceNone})
			if err != nil || got.Kind != Execute || got.Node.OwnerCapabilityPrefix != actor.CapabilityPrefix {
				t.Fatalf("self claim: %+v, %v", got, err)
			}
			before := *got.Node.LeaseExpiresAt
			*now = now.Add(time.Second)
			switch problem {
			case "dispatched":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET owner_capability_prefix='' WHERE run_id='run'`)
			case "expired":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET lease_expires_at=? WHERE run_id='run'`, now.UnixNano())
				before = *now
			case "revoked":
				if err := tenant.s.inTx(t.Context(), func(tx *storeTx) error {
					return tx.operator.revokeAuthorityTx(t.Context(), tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: got.Claim.CapabilityPrefix})
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, err = tenant.RenewOwner(t.Context(), actor)
			if problem == "expired" || problem == "revoked" {
				var refusal *ErrIllegalTransition
				if !errors.As(err, &refusal) || refusal.Reason != ReasonHolderRights {
					t.Fatalf("owner renewed its %s child: %v", problem, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			node, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			if (problem == "live") != node.LeaseExpiresAt.After(before) {
				t.Fatalf("renewed %s from %s to %s", problem, before, node.LeaseExpiresAt)
			}
		})
	}
}

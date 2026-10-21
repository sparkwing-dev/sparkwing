package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func recoveryFailureFixture(t *testing.T) (*Tenant, Actor, Actor, *nodeproto.JobFailure) {
	t.Helper()
	tenant, _, owner := reconcileFixture(t)
	holder := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claim"}, CapabilityPrefix: "claim", NodeFence: runspec.NodeFence{RunID: "run", NodeID: "job", Holder: "runner", Generation: 2}}
	root := authorityRoot("owner", "a")
	root.Subject.ID = "host"
	root.Grants = []capability.Grant{capability.TriggerHost, capability.NodesClaim, capability.DataRead}
	root.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "host", Generation: 1}
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM capabilities WHERE prefix='owner'`)
	authorityPersist(t, tenant.s, root)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,spec_hash,execution_json) VALUES ('a','run','recover','pending',?,'recovery',?)`, []byte(`{"id":"recover","on_failure_of":"job"}`), []byte(`{}`))
	failure := &nodeproto.JobFailure{Job: "job", Stage: "run", Error: &apierr.Error{Code: apierr.SDKError, Message: "masked failure", Details: map[string]any{"exit_code": float64(2)}}}
	return tenant, owner, holder, failure
}

func TestRecoveryReadsExactRecordedFailure(t *testing.T) {
	tenant, owner, holder, failure := recoveryFailureFixture(t)
	_, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: holder, To: NodeFailed, Cause: CauseReport, SourceAttempt: sourceAttempt(3), Extra: transitionExtra{Result: &NodeResult{Outcome: NodeFailed, MaskedError: failure.Error.Message, Failure: failure}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := tenant.ReadFailure(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"}, 2, owner)
	if err != nil || !reflect.DeepEqual(got, failure) {
		t.Fatalf("recorded failure: %+v, %v", got, err)
	}
	var raw []byte
	if err := tenant.s.db.QueryRow(`SELECT failure_json FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&raw); err != nil || !json.Valid(raw) {
		t.Fatalf("persisted failure: %s, %v", raw, err)
	}
}

func TestRecoveryFailureReadRechecksSourceAndReader(t *testing.T) {
	for _, problem := range []string{"generation", "nonterminal", "edge", "owner", "revoked", "grant", "foreign_team", "corrupt"} {
		t.Run(problem, func(t *testing.T) {
			tenant, owner, holder, failure := recoveryFailureFixture(t)
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: holder, To: NodeFailed, Cause: CauseReport, SourceAttempt: sourceAttempt(3), Extra: transitionExtra{Result: &NodeResult{Outcome: NodeFailed, Failure: failure}}}); err != nil {
				t.Fatal(err)
			}
			generation := int64(2)
			switch problem {
			case "generation":
				generation++
			case "nonterminal":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='pending',outcome='',finished_at=NULL WHERE node_id='job'`)
			case "edge":
				rewriteSchemaExec(t, tenant.s.db, `DELETE FROM nodes WHERE node_id='recover'`)
			case "owner":
				owner.Owner.Generation++
			case "revoked":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='owner'`)
			case "grant":
				root := authorityRoot("reader", "a")
				root.Binding = capability.Binding{Kind: capability.BindingNone}
				authorityPersist(t, tenant.s, root)
				owner.Authority.ID, owner.CapabilityPrefix = root.Prefix, root.Prefix
			case "foreign_team":
				tenant.team = "b"
			case "corrupt":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET failure_json=? WHERE node_id='job'`, []byte(`{`))
			}
			if _, err := tenant.ReadFailure(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"}, generation, owner); err == nil {
				t.Fatal("invalid recovery read succeeded")
			}
		})
	}
}

func TestRecoveryDoesNotManufactureFailureFromStatus(t *testing.T) {
	tenant, owner, holder, _ := recoveryFailureFixture(t)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: holder, To: NodeFailed, Cause: CauseReport, SourceAttempt: sourceAttempt(3), Extra: transitionExtra{Result: &NodeResult{Outcome: NodeFailed, Reason: "agent_lost", MaskedError: "text without structured failure"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := tenant.ReadFailure(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"}, 2, owner)
	if err != nil || got != nil {
		t.Fatalf("synthetic failure: %+v, %v", got, err)
	}
}

func TestRecoveryFailureReadRequiresTheClaimedJobsDeclaredEdge(t *testing.T) {
	tenant, owner, holder, failure := recoveryFailureFixture(t)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: holder, To: NodeFailed, Cause: CauseReport, SourceAttempt: sourceAttempt(3), Extra: transitionExtra{Result: &NodeResult{Outcome: NodeFailed, Failure: failure}}}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET ready_at=1 WHERE node_id='recover'`)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET expires_at=? WHERE id='run'`, tenant.s.now().Add(time.Hour).UnixNano())
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: owner.CapabilityPrefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "recover"}})
	if err != nil {
		t.Fatal(err)
	}
	reader := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
	got, err := tenant.ReadFailure(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"}, 2, reader)
	if err != nil || !reflect.DeepEqual(got, failure) {
		t.Fatalf("declared recovery read: %+v, %v", got, err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET spec_json=? WHERE node_id='recover'`, []byte(`{"id":"recover"}`))
	if _, err := tenant.ReadFailure(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"}, 2, reader); err == nil {
		t.Fatal("same-run holder read an undeclared failure")
	}
}

func TestInvalidRecordedFailureRollsBackTheResult(t *testing.T) {
	for _, problem := range []string{"job", "stage", "error", "success"} {
		t.Run(problem, func(t *testing.T) {
			tenant, _, holder, failure := recoveryFailureFixture(t)
			outcome := NodeFailed
			switch problem {
			case "job":
				failure.Job = "other"
			case "stage":
				failure.Stage = ""
			case "error":
				failure.Error = nil
			case "success":
				outcome = NodeSuccess
			}
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: holder, To: outcome, Cause: CauseReport, SourceAttempt: sourceAttempt(3), Extra: transitionExtra{Result: &NodeResult{Outcome: outcome, Failure: failure}}}); err == nil {
				t.Fatal("invalid structured failure was committed")
			}
			row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || row.Status != StatusRunning || row.StateSeq != 0 {
				t.Fatalf("invalid failure changed lifecycle: %+v, %v", row, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "events", 0)
		})
	}
}

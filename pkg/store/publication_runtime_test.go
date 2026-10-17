package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func publicationFixture(t *testing.T) (*Tenant, *time.Time, NodeTransition) {
	return publicationFixtureWithDispatch(t, runspec.DispatchHost)
}

func publicationFixtureWithDispatch(t *testing.T, dispatch runspec.Dispatch) (*Tenant, *time.Time, NodeTransition) {
	t.Helper()
	tenant, intake, now := intakeFixture(t)
	intake.Request.Mode = runspec.IntakePublish
	intake.Request.Dispatch = dispatch
	admitted, err := tenant.AdmitRun(t.Context(), intake)
	if err != nil {
		t.Fatal(err)
	}
	publisher := authorityRoot("publisher", "a")
	publisher.Grants = []capability.Grant{capability.DataWrite}
	publisher.Binding = capability.Binding{Kind: capability.BindingRun, RunID: admitted.RunID}
	publisher.Data = &capability.DataScope{Use: capability.DataPublish, RunID: admitted.RunID, RepositoryKey: "gh:1"}
	authorityPersist(t, tenant.s, publisher)
	actor := Actor{Kind: ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: publisher.Prefix}, CapabilityPrefix: publisher.Prefix}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "imported"}}}
	resolved, err := (fakeIntakeContracts{}).ResolvePlan(settings.PlanInputs{Request: admitted.Request, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tenant.AcceptPlan(t.Context(), PlanAcceptance{RunID: admitted.RunID, Actor: actor, Seal: runspec.SealInput{
		Request: admitted.Request, Plan: []byte(`{"jobs":[{"id":"imported"}]}`),
		Resolved: runspec.Resolved{Args: map[string]string{"message": "hello"}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, now, NodeTransition{Ref: NodeRef{admitted.RunID, "imported"}, Actor: actor, To: NodeSuccess, Cause: CausePublish, Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeSuccess}}}
}

func TestPublicationUsesNoControllerExecution(t *testing.T) {
	tenant, now, result := publicationFixtureWithDispatch(t, runspec.DispatchController)
	lease, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "replica", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionNode(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), result.Ref.RunID)
	if err != nil || run.State != RunPending || run.Owner != nil {
		t.Fatalf("controller settled publication before publisher finish: %+v, %v", run, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
	rewriteSchemaCount(t, tenant.s.db, "eval_units", 0)
	if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: run.ID, To: RunSuccess, Cause: RunFinish, Actor: Actor{Kind: ActorController, Owner: OwnerLeaseRef{Kind: OwnerController, Name: lease.Name, RunID: run.ID, Holder: lease.Holder, Generation: lease.Generation}}}); err == nil {
		t.Fatal("controller finished a published run")
	}
	*now = now.Add(time.Second)
	if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: run.ID, To: RunSuccess, Cause: RunPublish, Actor: result.Actor}); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationRequiresScopedAuthorityForJobsAndRun(t *testing.T) {
	for _, boundary := range []string{"job", "run"} {
		for _, problem := range []string{"grant", "binding", "scope", "data_run", "repository", "team", "audience", "revoked", "expired", "execute"} {
			t.Run(boundary+"/"+problem, func(t *testing.T) {
				tenant, now, result := publicationFixture(t)
				if boundary == "run" {
					if _, err := tenant.TransitionNode(t.Context(), result); err != nil {
						t.Fatal(err)
					}
				}
				c := authorityRoot("invalid-publisher", "a")
				c.Grants = []capability.Grant{capability.DataWrite}
				c.Binding = capability.Binding{Kind: capability.BindingRun, RunID: result.Ref.RunID}
				c.Data = &capability.DataScope{Use: capability.DataPublish, RunID: result.Ref.RunID, RepositoryKey: "gh:1"}
				switch problem {
				case "grant":
					c.Grants = []capability.Grant{capability.RunsWrite}
				case "binding":
					c.Binding = capability.Binding{Kind: capability.BindingNone}
				case "scope":
					c.Data.Use = capability.DataRemoteCache
				case "data_run":
					c.Data.RunID = "another"
				case "repository":
					c.Data.RepositoryKey = "gh:2"
				case "team":
					tenant = &Tenant{s: tenant.s, team: "b"}
				case "audience":
					c.Audience = capability.NodeHost
				case "revoked":
					c.RevokedAt = now
				case "expired":
					c.ExpiresAt = now
				case "execute":
					rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET intake_mode='execute' WHERE id=?`, result.Ref.RunID)
				}
				authorityPersist(t, tenant.s, c)
				result.Actor.Authority.ID, result.Actor.CapabilityPrefix = c.Prefix, c.Prefix
				before, err := tenant.ReadStateEvents(t.Context(), result.Ref.RunID, 0, 20)
				if err != nil {
					t.Fatal(err)
				}
				if boundary == "job" {
					_, err = tenant.TransitionNode(t.Context(), result)
				} else {
					_, err = tenant.TransitionRun(t.Context(), RunTransition{RunID: result.Ref.RunID, To: RunSuccess, Cause: RunPublish, Actor: result.Actor})
				}
				if err == nil {
					t.Fatal("invalid publisher committed")
				}
				after, err := tenant.ReadStateEvents(t.Context(), result.Ref.RunID, 0, 20)
				if err != nil || len(before) != len(after) {
					t.Fatalf("refused publication changed events: %v", err)
				}
			})
		}
	}
}

func TestPublicationRecordRefusalRollsBackResult(t *testing.T) {
	for _, mode := range []string{"missing_adapter", "refused", "expired"} {
		t.Run(mode, func(t *testing.T) {
			tenant, now, result := publicationFixture(t)
			result.Extra.Result.OutputRecord = &blob.RecordKey{Team: "a", Kind: blob.KindOutput, Name: "output"}
			before, err := tenant.ReadStateEvents(t.Context(), result.Ref.RunID, 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "missing_adapter" {
				tenant.s.resultRecords = nodeResultRecordsFunc(func(ctx context.Context, tx *storeTx, team Team, actor Actor, row NodeRow, record NodeResult) error {
					if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES ('published-record',?)`, []byte("yes")); err != nil {
						return err
					}
					if mode == "expired" {
						rewriteSchemaExec(t, tx, `UPDATE capabilities SET revoked_at=? WHERE prefix='publisher'`, now.UnixNano())
						return nil
					}
					return errors.New("record does not belong to publisher")
				})
			}
			if _, err := tenant.TransitionNode(t.Context(), result); err == nil {
				t.Fatal("invalid result committed")
			}
			row, err := tenant.ReadNode(t.Context(), result.Ref)
			if err != nil || row.Status != StatusPending || row.StateSeq != 0 {
				t.Fatalf("refused result changed job: %+v, %v", row, err)
			}
			after, err := tenant.ReadStateEvents(t.Context(), result.Ref.RunID, 0, 20)
			if err != nil || len(before) != len(after) {
				t.Fatalf("refused result changed events: %v", err)
			}
			rewriteSchemaCount(t, tenant.s.db, "settings", 0)
		})
	}
}

func TestPublicationCommitsResultsAndCompletionOnce(t *testing.T) {
	tenant, _, result := publicationFixture(t)
	for i := range 2 {
		applied, err := tenant.TransitionNode(t.Context(), result)
		if err != nil || applied.Replayed != (i == 1) {
			t.Fatalf("publish result %d: %+v, %v", i, applied, err)
		}
	}
	finish := RunTransition{RunID: result.Ref.RunID, Actor: result.Actor, To: RunSuccess, Cause: RunPublish}
	for i := range 2 {
		applied, err := tenant.TransitionRun(t.Context(), finish)
		if err != nil || applied.Replayed != (i == 1) {
			t.Fatalf("publish completion %d: %+v, %v", i, applied, err)
		}
	}
	run, err := tenant.ReadRun(t.Context(), result.Ref.RunID)
	if err != nil || run.State != RunSuccess || run.FinishedAt == nil || run.Owner != nil {
		t.Fatalf("published run: %+v, %v", run, err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), run.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Kind]++
	}
	if counts["job_succeeded"] != 1 || counts["run_success"] != 1 {
		t.Fatalf("publication lifecycle events: %v", counts)
	}
	rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
	rewriteSchemaCount(t, tenant.s.db, "node_attempts", 0)
	rewriteSchemaCount(t, tenant.s.db, "run_effects", 4)
	rewriteSchemaCount(t, tenant.s.db, "pipeline_success_pins", 0)
}

func TestPublicationCompletionChecksPersistedVerdict(t *testing.T) {
	tenant, _, result := publicationFixture(t)
	finish := RunTransition{RunID: result.Ref.RunID, Actor: result.Actor, To: RunSuccess, Cause: RunPublish}
	if _, err := tenant.TransitionRun(t.Context(), finish); err == nil {
		t.Fatal("publication completed before its jobs")
	}
	result.To, result.Extra.Result.Outcome = NodeFailed, NodeFailed
	if _, err := tenant.TransitionNode(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionRun(t.Context(), finish); err == nil {
		t.Fatal("failed published job produced a successful run")
	}
	finish.To = RunFailed
	if _, err := tenant.TransitionRun(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestAdmissionRefusesForeignAuthorityBeforePersistence(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("root", "b")
	root.Grants = []capability.Grant{capability.RunsWrite}
	authorityPersist(t, s, root)
	tenant := &Tenant{s: s, team: "a"}
	_, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}, Request: runspec.Request{Team: "a", Pipeline: "p", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost}})
	if err == nil {
		t.Fatal("foreign authority admitted a run")
	}
	rewriteSchemaCount(t, s.db, "runs", 0)
	rewriteSchemaCount(t, s.db, "triggers", 0)
}

type fakeIntakePolicy func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error)

func (f fakeIntakePolicy) ResolveScheduleSource(_ context.Context, _ CronSchedule, request runspec.Request) (runspec.Request, error) {
	request.Source.Ref = request.Git.SHA
	return request, nil
}

func (f fakeIntakePolicy) ResolveIntake(ctx context.Context, r capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
	facts, err := f(ctx, r, in)
	if facts.Options == nil {
		facts.Options = &in.Request.Options
	}
	return facts, err
}

func intakeFixture(t *testing.T, stores ...*Store) (*Tenant, IntakeRequest, *time.Time) {
	t.Helper()
	var s *Store
	if len(stores) == 0 {
		s = authorityStore(t)
	} else {
		s = stores[0]
		rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)")
		rewriteSchemaExec(t, s.db, "INSERT INTO team_counters(team) VALUES ('a'),('b')")
	}
	now := time.Unix(1700000000, 0).UTC()
	s.clock = func() time.Time { return now }
	root := authorityRoot("member", "a")
	root.Grants = []capability.Grant{capability.RunsWrite, capability.RunsControl}
	authorityPersist(t, s, root)
	s.intakePolicy = fakeIntakePolicy(func(ctx context.Context, r capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
		if _, err := r.ResolveAuthority(ctx, in.Authority); err != nil {
			return IntakePolicyFacts{}, err
		}
		return IntakePolicyFacts{LogRetentionClass: "r30", LogRetentionDays: 30}, nil
	})
	request := IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"}, Request: runspec.Request{Version: 1, Team: "a", Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, RequestedArgs: map[string]string{"message": "hello"}, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "main", SHA: "0123456789abcdef0123456789abcdef01234567"}, Source: runspec.Source{Ref: "0123456789abcdef0123456789abcdef01234567"}, Options: runspec.Options{OwnerExecutionTimeout: runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}}}}}
	return &Tenant{s: s, team: "a"}, request, &now
}

func planFixture(t *testing.T, dispatch runspec.Dispatch) (*Tenant, PlanAcceptance, *time.Time) {
	return planFixtureWithInput(t, dispatch, nil)
}

func planFixtureWithInput(t *testing.T, dispatch runspec.Dispatch, change func(*IntakeRequest), stores ...*Store) (*Tenant, PlanAcceptance, *time.Time) {
	t.Helper()
	tenant, in, now := intakeFixture(t, stores...)
	in.Request.Dispatch = dispatch
	if change != nil {
		change(&in)
	}
	admitted, err := tenant.AdmitRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	owner := authorityRoot("planner", "a")
	owner.Parent = &in.Authority
	owner.Issuance = capability.Award
	owner.Grants = []capability.Grant{capability.NodeResult}
	owner.Unit = capability.UnitTrigger
	owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: admitted.RunID, Holder: "host", Generation: 1}
	if dispatch == runspec.DispatchController {
		owner.Unit = capability.UnitPlan
		owner.Binding.Kind = capability.BindingRun
		rewriteSchemaExec(t, tenant.s.db, `INSERT INTO leases(name,holder,generation,until) VALUES ('controller.owner','controller',1,?)`, now.Add(time.Hour).UnixNano())
	}
	persistPlanOwner(t, tenant.s, owner)
	seedTriggerClaim(t, tenant.s, admitted.RunID, "host", "planner", 1, now.Add(time.Hour))
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}}
	raw, err := json.Marshal(testBodyPlan(plan))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Declarations: runspec.CheckoutDeclarations{Version: 1}, Request: admitted.Request, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	request := PlanAcceptance{RunID: admitted.RunID, Actor: runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "planner"}, CapabilityPrefix: "planner", Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(admitted.RunID), Holder: "host", Generation: 1}}}, Seal: runspec.SealInput{Declarations: runspec.CheckoutDeclarations{Version: 1}, Request: admitted.Request, Plan: raw, Resolved: runspec.Resolved{Args: map[string]string{"message": "hello"}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}}
	return tenant, request, now
}

func TestAdmissionAllocatesSealsAndIdempotencyAtomically(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	request.IdempotencyKey = "invocation"
	first, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Number != 1 || first.Request.RunID != first.RunID || first.OwnerUnit == nil || first.Request.Request.Actor.ID != "member" || first.Request.Hash == "" {
		t.Fatalf("admission=%+v", first)
	}
	replay, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || !replay.Replayed || replay.RunID != first.RunID {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	request.Request.RequestedArgs["message"] = "changed"
	if _, err = tenant.AdmitRun(t.Context(), request); err == nil {
		t.Fatal("changed body reused key")
	}
	request.IdempotencyKey = ""
	second, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || second.Number != 2 || second.RunID == first.RunID {
		t.Fatalf("second=%+v %v", second, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 2)
	rewriteSchemaCount(t, tenant.s.db, "triggers", 2)
}

func TestAdmissionPublishHasNoOwnerAndStillDeduplicates(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	request.Request.Mode = runspec.IntakePublish
	request.IdempotencyKey = "publish"
	first, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || first.OwnerUnit != nil {
		t.Fatalf("publish=%+v %v", first, err)
	}
	replay, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || !replay.Replayed || replay.RunID != first.RunID || replay.OwnerUnit != nil {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
}

func TestAdmissionRefusalsCommitNothing(t *testing.T) {
	for _, problem := range []string{"no_grant", "revoked", "wrong_team", "fork", "missing_policy", "policy_error", "missing_retention", "policy_expires_authority", "push_mismatch"} {
		t.Run(problem, func(t *testing.T) {
			tenant, request, now := intakeFixture(t)
			switch problem {
			case "no_grant":
				request.Authority.ID = "nobody"
				authorityPersist(t, tenant.s, authorityRoot("nobody", "a"))
			case "revoked":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='member'`)
			case "wrong_team":
				request.Request.Team = "b"
			case "fork":
				request.Request.PullRequest = runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{BaseRepo: "gh:1", HeadRepo: "gh:2"}}
			case "missing_policy":
				tenant.s.intakePolicy = nil
			case "policy_error":
				tenant.s.intakePolicy = fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
					return IntakePolicyFacts{}, errors.New("policy unavailable")
				})
			case "missing_retention":
				tenant.s.intakePolicy = fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
					return IntakePolicyFacts{}, nil
				})
			case "policy_expires_authority":
				root := authorityRoot("temporary", "a")
				root.Grants = []capability.Grant{capability.RunsWrite}
				expiry := now.Add(time.Second)
				root.ExpiresAt = &expiry
				authorityPersist(t, tenant.s, root)
				request.Authority.ID = "temporary"
				tenant.s.intakePolicy = fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
					*now = now.Add(time.Hour)
					return IntakePolicyFacts{LogRetentionClass: "r30", LogRetentionDays: 30}, nil
				})
			case "push_mismatch":
				root := authorityRoot("push", "a")
				root.Grants = []capability.Grant{capability.RunsWrite}
				root.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Ref: "main", SHA: "different", WorkflowRunID: "workflow", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}}
				authorityPersist(t, tenant.s, root)
				request.Authority.ID = "push"
			}
			if _, err := tenant.AdmitRun(t.Context(), request); err == nil {
				t.Fatal("invalid intake succeeded")
			}
			rewriteSchemaCount(t, tenant.s.db, "runs", 0)
			rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
			var n int64
			if err := tenant.s.db.QueryRow(`SELECT next_run_number FROM teams WHERE name='a'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("number=%d %v", n, err)
			}
		})
	}
}

func TestAdmissionPoolTrustRequiresIndependentFacts(t *testing.T) {
	for _, mode := range []string{"typed", "repository", "app", "forged_app", "fork_app"} {
		t.Run(mode, func(t *testing.T) {
			tenant, request, now := intakeFixture(t)
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO pool_access_rules(team,id,pool,repo_key,branch,created_by,created_at) VALUES ('a','repo','repo-pool','gh:1','','owner',1),('a','branch','branch-pool','gh:1','main','owner',1),('b','other','foreign-pool','gh:1','','owner',1)`)
			facts := IntakePolicyFacts{LogRetentionClass: "r30", LogRetentionDays: 30}
			if mode == "app" || mode == "fork_app" {
				request.VerifiedDeliveryID = "delivery"
				facts.Delivery = &VerifiedRunDelivery{Actor: runspec.Actor{Kind: "github", ID: "actor"}, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "refs/heads/main", SHA: fixtureOtherSHA, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Event: "push", RepositoryID: 1, DeliveryKey: "delivery", Ref: "refs/heads/main", SHA: fixtureOtherSHA, DefaultBranch: "main", VerifiedAt: *now}}}}
				if mode == "fork_app" {
					facts.Delivery.PullRequest = runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Fork: true}}
				}
			}
			request.Request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{DefaultBranchPush: true, Ref: "main", SHA: "typed"}}
			tenant.s.intakePolicy = fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
				return facts, nil
			})
			result, err := tenant.AdmitRun(t.Context(), request)
			if mode == "fork_app" {
				if err == nil {
					t.Fatal("verified fork admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{}
			if mode == "app" {
				want = []string{"branch-pool", "repo-pool"}
				if result.Request.Request.Git.SHA != fixtureOtherSHA || result.Request.Request.Actor.ID != "actor" {
					t.Fatal("App facts were not sealed")
				}
			}
			if !reflect.DeepEqual(result.Request.AllowedPools, want) {
				t.Fatalf("pools=%v want=%v", result.Request.AllowedPools, want)
			}
			if mode != "app" && result.Request.Request.Git.App.Set {
				t.Fatal("typed App facts survived intake")
			}
		})
	}
}

func TestPlanSealPersistsReplayAndExecutionSettings(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchHost)
	first, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Spec.Hash == "" || first.PlanHash == "" || first.Owner == nil {
		t.Fatalf("accepted=%+v", first)
	}
	node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"})
	if err != nil || node.SpecHash == "" || node.StateSeq != 0 || node.Status != runstate.StatusPending || !reflect.DeepEqual(node.Execution, first.Spec.Resolved.Settings.Jobs["job"]) {
		t.Fatalf("node=%+v %v", node, err)
	}
	request.Seal.Plan = []byte(` { "jobs": [ { "id": "job", "steps": [{"id":"step"}] } ] } `)
	replay, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil || !replay.Replayed || replay.Spec.Hash != first.Spec.Hash {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	request.Seal.Resolved.Entrypoint = "other"
	if _, err := tenant.AcceptPlan(t.Context(), request); err == nil {
		t.Fatal("changed spec accepted")
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 1)
}

func TestAcceptedPlanAndSnapshotReturnIndependentValues(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchHost)
	request.Seal.Plan = []byte(`{"jobs":[{"id":"job","steps":[{"id":"step"}],"requires":["linux"]}]}`)
	var proposed nodeproto.PlanDoc
	if err := json.Unmarshal(request.Seal.Plan, &proposed); err != nil {
		t.Fatal(err)
	}
	request.Seal.Resolved.Settings, _ = settings.ResolvePlan(settings.PlanInputs{Request: request.Seal.Request, Declarations: request.Seal.Declarations, Plan: proposed})
	accepted, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := accepted.Spec.Hash
	accepted.Spec.Resolved.Args["message"] = "changed"
	accepted.Spec.Resolved.Settings.Jobs["job"].Requires.Value[0] = "changed"
	accepted.Plan[0] = '!'
	snapshot, err := tenant.ReadSnapshot(t.Context(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Run.Request.Request.RequestedArgs["message"] = "changed"
	snapshot.Run.Spec.Resolved.Args["message"] = "changed"
	snapshot.Run.Plan[0] = '!'
	snapshot.Nodes[0].Spec[0] = '!'
	snapshot.Nodes[0].Execution.Requires.Value[0] = "changed"

	again, err := tenant.ReadSnapshot(t.Context(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Run.Request.Request.RequestedArgs["message"] != "hello" || again.Run.Spec.Resolved.Args["message"] != "hello" || again.Run.Spec.Hash != wantHash || !reflect.DeepEqual(again.Nodes[0].Execution.Requires.Value, []string{"linux"}) {
		t.Fatalf("returned values changed persisted inputs: %+v", again)
	}
	var plan nodeproto.PlanDoc
	var job nodeproto.JobSpec
	if err := json.Unmarshal(again.Run.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(again.Nodes[0].Spec, &job); err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].ID != "job" || job.ID != "job" || !reflect.DeepEqual(job.Requires, []string{"linux"}) {
		t.Fatalf("returned values changed persisted declarations: plan=%+v job=%+v", plan, job)
	}
}

func TestPlanSealRefusalRollsBackBothSealAndJobs(t *testing.T) {
	for _, problem := range []string{"request", "argument", "environment", "unclassified", "settings_drift", "missing_job_settings", "duplicate_job", "invalid_graph", "missing_secret", "secret_identifier", "expired", "revoked_parent", "cancelled"} {
		t.Run(problem, func(t *testing.T) {
			tenant, request, now := planFixture(t, runspec.DispatchHost)
			switch problem {
			case "request":
				request.Seal.Request.Request.Git.SHA = "changed"
			case "argument":
				request.Seal.Resolved.Args["message"] = "changed"
			case "environment":
				request.Seal.Resolved.Env.Names = []string{"LEAK"}
			case "unclassified":
				request.Seal.Resolved.SecretArgs = nil
			case "settings_drift":
				request.Seal.Resolved.Settings.Options.Priority = runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 99}}
			case "missing_job_settings":
				request.Seal.Resolved.Settings.Jobs = nil
			case "duplicate_job":
				request.Seal.Plan = []byte(`{"jobs":[{"id":"job","steps":[{"id":"step"}]},{"id":"job","steps":[{"id":"step"}]}]}`)
			case "invalid_graph":
				request.Seal.Plan = []byte(`{"jobs":[{"id":"job","steps":[{"id":"step"}],"deps":["absent"]}]}`)
			case "missing_secret":
				request.Seal.Resolved.SecretNames = []string{"absent"}
			case "secret_identifier":
				request.Seal.Resolved.SecretArgs = []string{"message"}
				request.Seal.Plan = []byte(`{"jobs":[{"id":"hello","steps":[{"id":"step"}]}]}`)
			case "expired":
				*now = now.Add(time.Hour)
			case "revoked_parent":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='member'`)
			case "cancelled":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=1 WHERE id=?`, request.RunID)
			}
			if _, err := tenant.AcceptPlan(t.Context(), request); err == nil {
				t.Fatal("invalid plan succeeded")
			}
			run, err := tenant.ReadRun(t.Context(), request.RunID)
			if err != nil || run.Spec != nil {
				t.Fatalf("partial seal=%+v %v", run.Spec, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
		})
	}
}

func TestPlanClassificationNeverShrinks(t *testing.T) {
	tenant, in, _ := intakeFixture(t)
	in.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"message"}}
	admitted, err := tenant.AdmitRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	root := authorityRoot("planner", "a")
	root.Grants = []capability.Grant{capability.NodeResult}
	root.Unit = capability.UnitTrigger
	root.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: admitted.RunID, Holder: "host", Generation: 1}
	persistPlanOwner(t, tenant.s, root)
	seedTriggerClaim(t, tenant.s, admitted.RunID, "host", "planner", 1, tenant.s.now().Add(time.Hour))
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}
	resolved, _ := settings.ResolvePlan(settings.PlanInputs{Declarations: runspec.CheckoutDeclarations{Version: 1}, Request: admitted.Request, Plan: plan})
	request := PlanAcceptance{RunID: admitted.RunID, Actor: runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "planner"}, CapabilityPrefix: "planner", Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(admitted.RunID), Holder: "host", Generation: 1}}}, Seal: runspec.SealInput{Declarations: runspec.CheckoutDeclarations{Version: 1}, Request: admitted.Request, Plan: []byte(`{"jobs":[]}`), Resolved: runspec.Resolved{Args: map[string]string{"message": "hello"}, SecretArgs: []string{}, Settings: resolved}}}
	accepted, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accepted.Spec.Resolved.SecretArgs, []string{"message"}) {
		t.Fatalf("classification=%v", accepted.Spec.Resolved.SecretArgs)
	}
}

func TestPlanControllerHandoffKeepsExactReplay(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchController)
	accepted, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Owner == nil || accepted.Owner.Kind != runstate.OwnerController || accepted.Owner.Holder != "controller" {
		t.Fatalf("owner=%+v", accepted.Owner)
	}
	var status string
	if err := tenant.s.db.QueryRow(`SELECT status FROM triggers WHERE id=?`, request.RunID).Scan(&status); err != nil || status != "done" {
		t.Fatalf("owner unit=%s %v", status, err)
	}
	var holder, prefix string
	if err := tenant.s.db.QueryRow(`SELECT COALESCE(claimed_by,''),capability_prefix FROM triggers WHERE id=?`, request.RunID).Scan(&holder, &prefix); err != nil || holder != "" || prefix != "" {
		t.Fatalf("finished owner still held: %q %q %v", holder, prefix, err)
	}
	node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"})
	if err != nil || node.ReadyAt == nil {
		t.Fatalf("controller did not schedule: %+v %v", node, err)
	}
	replay, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay=%+v %v", replay, err)
	}
}

func TestPlanControllerHandoffRequiresLiveOwnerLease(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchController)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET until=1 WHERE name='controller.owner'`)
	if _, err := tenant.AcceptPlan(t.Context(), request); err == nil {
		t.Fatal("plan owner released without controller owner")
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.Spec != nil {
		t.Fatalf("partial seal=%+v %v", run.Spec, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
}

func TestAdmissionRetryCarriesSealsAndKeepsRetrierSeparate(t *testing.T) {
	tenant, plan, _ := planFixtureWithInput(t, runspec.DispatchHost, func(in *IntakeRequest) {
		in.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{}}
		in.Request.PullRequest = runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Number: 42, BaseRepo: "gh:1", HeadRepo: "gh:1", BaseRef: "main", HeadRef: "feature", HeadSHA: "0123456789abcdef0123456789abcdef01234567"}}
	})
	if runspec.DisplayRequest(plan.Seal.Request).RequestedArgs["message"] != "hello" {
		t.Fatal("intake fixture must initially classify message as public")
	}
	plan.Seal.Resolved.SecretArgs = []string{"message"}
	retrier := authorityRoot("retrier", "a")
	retrier.Grants = []capability.Grant{capability.RunsControl}
	authorityPersist(t, tenant.s, retrier)
	source, err := tenant.AcceptPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	request := IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "retrier"}, IdempotencyKey: "retry", Request: runspec.Request{Pipeline: "p", Lineage: runspec.Lineage{RetryOf: plan.RunID}, Git: runspec.Git{SHA: "new-head"}}}
	retry, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Inputs.RequestedArgs["message"] != runspec.RedactedArgValue || retry.Inputs.Args["message"] != runspec.RedactedArgValue {
		t.Fatal("retry receipt ignored accepted secret classification")
	}
	run, err := tenant.ReadRun(t.Context(), retry.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Spec == nil || !reflect.DeepEqual(*run.Spec, source.Spec) || !reflect.DeepEqual(run.Request, source.Spec.Request) || run.Lineage.RetryOf != plan.RunID || !run.Lineage.Retrier.Set || run.Lineage.Retrier.Value.ID != "retrier" || run.Request.Request.Actor.ID != "member" || !run.Request.Request.PullRequest.Set || run.Request.Request.PullRequest.Value.Number != 42 {
		t.Fatalf("retry lost seals/lineage: %+v", run)
	}
	node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(retry.RunID), NodeID: "job"})
	if err != nil || node.Generation != 0 || node.Status != runstate.StatusPending {
		t.Fatalf("retry job=%+v %v", node, err)
	}
	replay, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || !replay.Replayed || replay.RunID != retry.RunID || replay.Inputs.RequestedArgs["message"] != runspec.RedactedArgValue || replay.Inputs.Args["message"] != runspec.RedactedArgValue {
		t.Fatalf("retry replay=%+v %v", replay, err)
	}
}

func TestAdmissionHereAwardRetainsRequestingAuthority(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	request.IdempotencyKey = "here"
	admitted, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || len(admitted.HereBearer) == 0 || admitted.HereCapabilityPrefix == "" {
		t.Fatalf("here=%+v %v", admitted, err)
	}
	err = tenant.s.inTx(t.Context(), func(tx *storeTx) error {
		p, err := tx.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: admitted.HereCapabilityPrefix})
		if err != nil {
			return err
		}
		c := p.Capability
		if c.Purpose != capability.PurposeHere || c.Parent == nil || *c.Parent != request.Authority || c.Binding.RunID != admitted.RunID || c.Binding.ClaimBy == nil {
			t.Fatalf("here authority=%+v", c)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || !replay.Replayed || replay.HereCapabilityPrefix != admitted.HereCapabilityPrefix || len(replay.HereBearer) != 0 {
		t.Fatalf("here replay=%+v %v", replay, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "capabilities", 2)
}

func TestLocalForegroundClaimCannotLoseToBackgroundQueue(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	request.Request.Origin = runspec.OriginLocal
	request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	admitted, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || admitted.HereCapabilityPrefix == "" {
		t.Fatalf("local admission: %v", err)
	}
	background := authorityRoot("local-queue", "a")
	background.Subject.Kind = capability.SubjectLocal
	background.Grants = []capability.Grant{capability.TriggersClaim}
	authorityPersist(t, tenant.s, background)
	op := tenant.s.AsOperator()
	if claim, err := op.ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: background.Prefix, Kinds: []capability.UnitKind{capability.UnitTrigger}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("background queue took foreground work: %+v %v", claim, err)
	}
	target := &UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(admitted.RunID)}
	if _, err := op.ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: background.Prefix, Target: target}); err == nil {
		t.Fatal("named background claim bypassed the foreground reservation")
	}
	claim, err := op.ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: admitted.HereCapabilityPrefix, Target: target})
	if err != nil || claim.RunID != admitted.RunID || claim.Award.Generation != 1 {
		t.Fatalf("foreground handoff lost its first award: %+v %v", claim, err)
	}
}

func TestLocalForegroundReservationUsesTheRequestingOSAccount(t *testing.T) {
	for _, foreground := range []bool{false, true} {
		t.Run(map[bool]string{false: "detached", true: "foreground"}[foreground], func(t *testing.T) {
			tenant, request, _ := intakeFixture(t)
			requester := authorityRoot("local-requester", "a")
			requester.Subject.Kind = capability.SubjectLocal
			requester.Grants = []capability.Grant{capability.RunsWrite, capability.TriggersClaim}
			authorityPersist(t, tenant.s, requester)
			request.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: requester.Prefix}
			request.Request.Origin, request.Request.LocalForeground = runspec.OriginLocal, foreground
			request.Request.Dispatch = ""
			admitted, err := tenant.AdmitRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if admitted.Request.Request.Dispatch != runspec.DispatchHost {
				t.Fatalf("local dispatch=%q, want host", admitted.Request.Request.Dispatch)
			}
			background := authorityRoot("local-queue", "a")
			background.Subject, background.Grants = requester.Subject, requester.Grants
			authorityPersist(t, tenant.s, background)
			demand := UnitDemand{CapabilityPrefix: background.Prefix, Kinds: []capability.UnitKind{capability.UnitTrigger}}
			claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), demand)
			if !foreground {
				if err != nil || claim.RunID != admitted.RunID {
					t.Fatalf("detached queue: %+v %v", claim, err)
				}
				return
			}
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("background took reserved local work: %+v %v", claim, err)
			}
			demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(admitted.RunID)}
			other := authorityRoot("another-account", "a")
			other.Subject.Kind, other.Grants = capability.SubjectLocal, requester.Grants
			authorityPersist(t, tenant.s, other)
			demand.CapabilityPrefix = other.Prefix
			if _, err := tenant.s.AsOperator().ClaimUnit(t.Context(), demand); err == nil {
				t.Fatal("another OS account took the foreground reservation")
			}
			demand.CapabilityPrefix = requester.Prefix
			claim, err = tenant.s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || claim.Award.Generation != 1 {
				t.Fatalf("foreground claim: %+v %v", claim, err)
			}
			if err := tenant.s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
				t.Fatal(err)
			}
			demand.CapabilityPrefix, demand.Target = background.Prefix, nil
			claim, err = tenant.s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || claim.Award.Generation != 2 {
				t.Fatalf("background reclaim after handoff: %+v %v", claim, err)
			}
		})
	}
}

func TestPlanPublishRequiresRunBoundPublisher(t *testing.T) {
	for _, mode := range []string{"valid", "member", "foreign_run", "wrong_scope", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			tenant, intake, _ := intakeFixture(t)
			intake.Request.Mode = runspec.IntakePublish
			admitted, err := tenant.AdmitRun(t.Context(), intake)
			if err != nil {
				t.Fatal(err)
			}
			c := authorityRoot("publisher", "a")
			c.Grants = []capability.Grant{capability.DataWrite}
			c.Binding = capability.Binding{Kind: capability.BindingRun, RunID: admitted.RunID}
			c.Data = &capability.DataScope{Use: capability.DataPublish, RunID: admitted.RunID, RepositoryKey: "gh:1"}
			switch mode {
			case "member":
				c.Binding = capability.Binding{Kind: capability.BindingNone}
			case "foreign_run":
				foreign, err := tenant.AdmitRun(t.Context(), intake)
				if err != nil {
					t.Fatal(err)
				}
				c.Binding.RunID = foreign.RunID
			case "wrong_scope":
				c.Data.Use = capability.DataRemoteCache
			}
			authorityPersist(t, tenant.s, c)
			if mode == "revoked" {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='publisher'`)
			}
			jobs := []nodeproto.JobSpec{{ID: "imported"}}
			plan := nodeproto.PlanDoc{Jobs: jobs}
			resolved, err := settings.ResolvePlan(settings.PlanInputs{Declarations: runspec.CheckoutDeclarations{Version: 1}, Request: admitted.Request, Plan: plan})
			if err != nil {
				t.Fatal(err)
			}
			request := PlanAcceptance{RunID: admitted.RunID, Actor: runstate.Actor{Kind: runstate.ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "publisher"}, CapabilityPrefix: "publisher"}, Seal: runspec.SealInput{Declarations: runspec.CheckoutDeclarations{Version: 1}, Request: admitted.Request, Plan: []byte(`{"jobs":[{"id":"imported","steps":[{"id":"step"}]}]}`), Resolved: runspec.Resolved{Args: map[string]string{"message": "hello"}, SecretArgs: []string{}, Settings: resolved}}}
			accepted, err := tenant.AcceptPlan(t.Context(), request)
			if mode != "valid" {
				if err == nil {
					t.Fatal("unbound publisher accepted")
				}
				rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
				return
			}
			if err != nil || accepted.Owner != nil {
				t.Fatalf("published plan=%+v %v", accepted, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
			node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(admitted.RunID), NodeID: "imported"})
			if err != nil || node.ClaimedBy != "" || node.Generation != 0 {
				t.Fatalf("published job=%+v %v", node, err)
			}
		})
	}
}

func TestPlanSealRechecksAncestorAfterSecretDecryption(t *testing.T) {
	tenant, request, now := planFixture(t, runspec.DispatchHost)
	root := authorityRoot("short", "a")
	root.Grants = []capability.Grant{capability.NodeResult}
	expiry := now.Add(time.Second)
	root.ExpiresAt = &expiry
	authorityPersist(t, tenant.s, root)
	owner := authorityRoot("short-planner", "a")
	owner.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "short"}
	owner.Issuance = capability.Award
	owner.Grants = []capability.Grant{capability.NodeResult}
	owner.Unit = capability.UnitTrigger
	owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: request.RunID, Holder: "host", Generation: 1}
	persistPlanOwner(t, tenant.s, owner)
	request.Actor.Authority.ID, request.Actor.CapabilityPrefix = "short-planner", "short-planner"
	rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET capability_prefix='short-planner' WHERE id=?`, request.RunID)
	expirePlanSecret(t, tenant, &request, func() { *now = now.Add(2 * time.Second) })
	if _, err := tenant.AcceptPlan(t.Context(), request); err == nil {
		t.Fatal("expired ancestor authorized acceptance")
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
}

func TestPlanControllerEmptyPlanReplayAfterSettlement(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchController)
	request.Seal.Plan = []byte(`{"jobs":[]}`)
	request.Seal.Resolved.Settings.Jobs = map[string]runspec.JobExecutionSettings{}
	first, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.State != runstate.RunSuccess {
		t.Fatalf("settlement=%+v %v", run, err)
	}
	replay, err := tenant.AcceptPlan(t.Context(), request)
	if err != nil || !replay.Replayed || replay.Spec.Hash != first.Spec.Hash {
		t.Fatalf("settled replay=%+v %v", replay, err)
	}
}

func TestAdmissionConcurrentNumberingAndReplay(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	results := make(chan AdmissionResult, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { result, err := tenant.AdmitRun(t.Context(), request); results <- result; failures <- err }()
	}
	numbers := map[int64]bool{}
	for i := 0; i < 8; i++ {
		result := <-results
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
		if numbers[result.Number] {
			t.Fatalf("duplicate number %d", result.Number)
		}
		numbers[result.Number] = true
	}
	request.IdempotencyKey = "same-key"
	for i := 0; i < 8; i++ {
		go func() { result, err := tenant.AdmitRun(t.Context(), request); results <- result; failures <- err }()
	}
	var id string
	for i := 0; i < 8; i++ {
		result := <-results
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
		if id != "" && result.RunID != id {
			t.Fatal("idempotent intake split across runs")
		}
		id = result.RunID
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 9)
}

func TestAdmissionRouteNarrowsSealedRequirements(t *testing.T) {
	// Reserved pools route only verified provenance, so the run is a signed App push.
	s, tenant, request := appAdmissionFixture(t, true, "signed")
	defer s.Close()
	request.Request.Options.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"linux"}}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "runner"}}
	rewriteSchemaExec(t, s.db, `INSERT INTO pool_access_rules(team,id,pool,repo_key,route,created_by,created_at) VALUES ('a','route','prod','gh:701','true','owner',1)`)
	result, err := tenant.AdmitGitHubAppRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Request.Request.Options.Requires.Value, []string{"linux", "pool=prod"}) || !reflect.DeepEqual(result.Request.AllowedPools, []string{"prod"}) || result.Request.Request.Options.Requires.Origin.Layer != runspec.LayerFlag {
		t.Fatalf("routing=%+v pools=%v", result.Request.Request.Options.Requires, result.Request.AllowedPools)
	}
	if !reflect.DeepEqual(request.Request.Options.Requires.Value, []string{"linux"}) {
		t.Fatal("intake mutated caller options")
	}
}

func TestPlanConcurrentSealCannotShrinkClassification(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchHost)
	classified := request
	classified.Seal.Resolved.SecretArgs = []string{"message"}
	results := make(chan error, 2)
	go func() { _, err := tenant.AcceptPlan(t.Context(), classified); results <- err }()
	go func() { _, err := tenant.AcceptPlan(t.Context(), request); results <- err }()
	success := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("conflicting seals succeeded %d times", success)
	}
	before, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tenant.AcceptPlan(t.Context(), request)
	after, readErr := tenant.ReadRun(t.Context(), request.RunID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(before.Spec, after.Spec) {
		t.Fatal("replay changed accepted classification")
	}
	if len(before.Spec.Resolved.SecretArgs) > 0 && err == nil {
		t.Fatal("classified seal accepted an unclassified replacement")
	}
}

func persistPlanOwner(t *testing.T, s *Store, c capability.Capability) {
	t.Helper()
	c.Issuance = capability.Award
	if c.Parent == nil {
		c.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"}
	}
	c.Purpose = capability.PurposeTrigger
	billing := capability.BillingHost
	if c.Unit == capability.UnitPlan {
		c.Purpose = capability.PurposePlan
		billing = capability.BillingPlan
	}
	c.UnitID = c.Binding.RunID
	c.Delivery = capability.DeliveryDirect
	c.Billing = &capability.BillingProvenance{PayerTeam: c.Team, ReservationID: "award:" + c.Prefix, ExecutorClass: "runner", Start: capability.BillingAtAward, Unit: billing}
	authorityPersist(t, s, c)
}

func TestPlanGuardRefusalPersistsSealWithoutOfferingJobs(t *testing.T) {
	for _, dispatch := range []runspec.Dispatch{runspec.DispatchHost, runspec.DispatchController} {
		t.Run(string(dispatch), func(t *testing.T) {
			tenant, request, _ := planFixture(t, dispatch)
			request.Seal.Resolved.Guards = runspec.Guards{When: []string{"git:branch=default"}, Passed: false, Reason: "default branch is unresolved"}
			accepted, err := tenant.AcceptPlan(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if accepted.Spec.Resolved.Guards.Passed || accepted.Spec.Resolved.Guards.Reason == "" {
				t.Fatal("refused guard was not sealed")
			}
			snapshot, err := tenant.ReadSnapshot(t.Context(), request.RunID)
			if err != nil {
				t.Fatal(err)
			}
			graph, err := snapshot.Scheduling()
			if err != nil {
				t.Fatal(err)
			}
			for _, change := range Decide(graph).Nodes {
				if change.Kind == DecisionReady || change.Kind == DecisionExpand || change.Kind == DecisionApproval {
					t.Fatalf("guard refusal offered work: %+v", change)
				}
			}
			if dispatch == runspec.DispatchController {
				if snapshot.Run.State != runstate.RunFailed || snapshot.Nodes[0].Outcome != runstate.NodeSkipped {
					t.Fatalf("guard refusal settlement=%s %+v", snapshot.Run.State, snapshot.Nodes)
				}
			} else {
				_, err = tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"}, To: runstate.NodeReady, Cause: runstate.CauseRelease, Actor: request.Actor})
				if err == nil {
					t.Fatal("guard-refused job became offerable")
				}
			}
		})
	}
}

func TestAdmissionRetryKeepsVerifiedAppFactsAndDoesNotReplayDelivery(t *testing.T) {
	tenant, request, now := intakeFixture(t)
	request.VerifiedDeliveryID = "delivery"
	facts := IntakePolicyFacts{LogRetentionClass: "r30", LogRetentionDays: 30, Delivery: &VerifiedRunDelivery{Actor: runspec.Actor{Kind: "github", ID: "original-actor"}, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "main", SHA: fixtureFeatureSHA, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{DeliveryKey: "delivery", RepositoryID: 1, Ref: "main", SHA: fixtureFeatureSHA, DefaultBranch: "main", DefaultBranchPush: true, VerifiedAt: *now}}}}}
	tenant.s.intakePolicy = fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
		return facts, nil
	})
	original, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.VerifiedDeliveryID = ""
	request.Request.Lineage.RetryOf = original.RunID
	request.Request.Git.SHA = fixtureOtherSHA
	retry, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retry.Request, original.Request) || retry.Request.Request.Git.SHA != fixtureFeatureSHA || !retry.Request.Request.Git.App.Value.DefaultBranchPush || retry.Request.Request.Actor.ID != "original-actor" {
		t.Fatalf("retry changed original authority: %+v", retry.Request)
	}
	var delivery string
	if err := tenant.s.db.QueryRow(`SELECT webhook_delivery FROM triggers WHERE id=?`, retry.RunID).Scan(&delivery); err != nil || delivery != "" {
		t.Fatalf("retry reprocessed delivery: %q %v", delivery, err)
	}
}

func TestAdmissionDeduplicatesVerifiedAppDeliveryWithoutClientKey(t *testing.T) {
	tenant, request, now := intakeFixture(t)
	request.VerifiedDeliveryID = "delivery"
	tenant.s.intakePolicy = fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
		return IntakePolicyFacts{LogRetentionClass: "r30", LogRetentionDays: 30, Delivery: &VerifiedRunDelivery{Actor: runspec.Actor{Kind: "github", ID: "original-actor"}, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "main", SHA: fixtureFeatureSHA, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{DeliveryKey: "delivery", RepositoryID: 1, Ref: "main", SHA: fixtureFeatureSHA, DefaultBranch: "main", DefaultBranchPush: true, VerifiedAt: *now}}}}}, nil
	})
	first, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || !replay.Replayed || replay.RunID != first.RunID || !reflect.DeepEqual(replay.Request, first.Request) {
		t.Fatalf("delivery replay = %q, replayed=%v, %v", replay.RunID, replay.Replayed, err)
	}
	type admission struct {
		result AdmissionResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan admission, 4)
	for range 4 {
		go func() {
			<-start
			result, err := tenant.AdmitRun(t.Context(), request)
			results <- admission{result, err}
		}()
	}
	close(start)
	for range 4 {
		got := <-results
		if got.err != nil || !got.result.Replayed || got.result.RunID != first.RunID {
			t.Errorf("concurrent redelivery = %q, replayed=%v, %v", got.result.RunID, got.result.Replayed, got.err)
		}
	}
	request.Request.Pipeline = "other"
	other, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || other.Replayed || other.RunID == first.RunID || other.Number != first.Number+1 {
		t.Fatalf("delivery's other pipeline = %q, number=%d, replayed=%v, %v", other.RunID, other.Number, other.Replayed, err)
	}
}

func TestAdmissionSealsServerResolvedOptionsAndCeilings(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	ceiling := runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 4}, Origin: runspec.SettingOrigin{Layer: runspec.LayerOperatorTeam, Key: "operator.max_priority"}}
	tenant.s.intakePolicy = fakeIntakePolicy(func(_ context.Context, _ capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
		options := in.Request.Options
		options.Priority = runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 0}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "priority"}}
		return IntakePolicyFacts{Options: &options, Ceilings: runspec.Ceilings{Priority: ceiling}, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
	})
	result, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Request.Request.Options.Priority.Set || result.Request.Request.Options.Priority.Value != 0 || !reflect.DeepEqual(result.Request.Ceilings.Priority, ceiling) {
		t.Fatalf("policy seal=%+v", result.Request)
	}
}

func testBodyPlan(plan nodeproto.PlanDoc) nodeproto.PlanDoc {
	for i := range plan.Jobs {
		job := &plan.Jobs[i]
		if job.Approval == nil && len(job.Children) == 0 && len(job.Steps) == 0 {
			job.Steps = []nodeproto.StepSpec{{ID: "step"}}
		}
	}
	return plan
}

func expirePlanSecret(t *testing.T, tenant *Tenant, request *PlanAcceptance, expire func()) {
	t.Helper()
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO secrets(team,name,value,principal,created_at,updated_at,masked,pipeline,shared,only_on) VALUES ('a','clock-secret','ciphertext','account',1,1,1,'p',0,'')`)
	request.Seal.Resolved.SecretNames = []string{"clock-secret"}
	tenant.s.planSecretOpener = fakePlanSecretOpener(func(context.Context, Team, Secret) (string, error) {
		expire()
		return "clock-secret-value", nil
	})
}

func TestPlanSealRechecksOwnerAfterSecretDecryption(t *testing.T) {
	tenant, request, now := planFixture(t, runspec.DispatchHost)
	expirePlanSecret(t, tenant, &request, func() { *now = now.Add(time.Hour) })
	if _, err := tenant.AcceptPlan(t.Context(), request); err == nil {
		t.Fatal("expired owner committed plan jobs")
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.Spec != nil {
		t.Fatalf("expired acceptance changed spec: %+v %v", run.Spec, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
}

func TestAdmissionRechecksAuthorityAfterPolicyWork(t *testing.T) {
	tenant, request, now := intakeFixture(t)
	root := authorityRoot("short-member", "a")
	root.Grants = []capability.Grant{capability.RunsWrite}
	expires := now.Add(time.Second)
	root.ExpiresAt = &expires
	authorityPersist(t, tenant.s, root)
	request.Authority.ID = root.Prefix
	policy := tenant.s.intakePolicy
	tenant.s.intakePolicy = fakeIntakePolicy(func(ctx context.Context, resolver capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
		facts, err := policy.ResolveIntake(ctx, resolver, in)
		*now = expires
		return facts, err
	})
	_, err := tenant.AdmitRun(t.Context(), request)
	var denied *apierr.Error
	if !errors.As(err, &denied) || denied.Code != apierr.Unauthenticated {
		t.Fatalf("expired intake authority = %v", err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 0)
}

func seedTriggerClaim(t *testing.T, s *Store, runID, holder, prefix string, generation int64, expiry time.Time) {
	t.Helper()
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET status='claimed',claimed_by=?,capability_prefix=?,generation=?,claimed_at=?,lease_expires_at=? WHERE id=?`, holder, prefix, generation, s.now().UnixNano(), expiry.UnixNano(), runID)
}

func TestLocalForegroundReservationDoesNotFollowRetrySeals(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	root := authorityRoot("local-submitter", "a")
	root.Subject.Kind = capability.SubjectLocal
	root.Grants = []capability.Grant{capability.RunsWrite, capability.RunsControl, capability.TriggersClaim}
	authorityPersist(t, tenant.s, root)
	request.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	request.Request.Origin, request.Request.LocalForeground = runspec.OriginLocal, true
	original, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: request.Authority, RetryMode: "all", Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: original.RunID}}})
	if err != nil {
		t.Fatal(err)
	}
	if retry.Request.RunID != original.RunID || !retry.Request.Request.LocalForeground {
		t.Fatal("retry changed its immutable source seal")
	}
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Kinds: []capability.UnitKind{capability.UnitTrigger}})
	if err != nil || claim.RunID != retry.RunID {
		t.Fatalf("retry inherited foreground reservation: run=%s err=%v", claim.RunID, err)
	}
}

func TestLocalForegroundIntakeRequiresLocalHostExecution(t *testing.T) {
	for _, invalid := range []string{"subject", "origin", "dispatch", "mode"} {
		t.Run(invalid, func(t *testing.T) {
			tenant, request, _ := intakeFixture(t)
			root := authorityRoot("local-submitter", "a")
			root.Subject.Kind = capability.SubjectLocal
			root.Grants = []capability.Grant{capability.RunsWrite}
			if invalid == "subject" {
				root.Subject.Kind = capability.SubjectMachine
			}
			authorityPersist(t, tenant.s, root)
			request.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
			request.Request.Origin, request.Request.LocalForeground = runspec.OriginLocal, true
			switch invalid {
			case "origin":
				request.Request.Origin = runspec.OriginTrigger
			case "dispatch":
				request.Request.Dispatch = runspec.DispatchController
			case "mode":
				request.Request.Mode = runspec.IntakePublish
			}
			if _, err := tenant.AdmitRun(t.Context(), request); err == nil {
				t.Fatal("invalid foreground reservation admitted")
			}
			rewriteSchemaCount(t, tenant.s.db, "runs", 0)
		})
	}
}

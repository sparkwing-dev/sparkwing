package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type engineFixture struct {
	*EngineController
	Store         *store.Store
	Server        *Server
	claimProfiles store.ClaimProfileResolver
}

func engineRuntimeStore(t *testing.T, opts ...store.Option) (*engineFixture, *store.Tenant) {
	t.Helper()
	fixture := &engineFixture{claimProfiles: engineProfileFake{}}
	profiles := store.WithPolicies(func(st *store.Store) (store.Policies, error) {
		fixture.Server = NewDeployment(st, deploymenttest.Metered(t), nil)
		policies := fixture.Server.StorePolicies()
		policies.ClaimProfiles = engineEnrollmentFunc(func(ctx context.Context, p capability.Principal) (match.Profile, error) {
			return fixture.claimProfiles.ResolveClaimProfile(ctx, p)
		})
		return policies, nil
	})
	s, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), append([]store.Option{profiles}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	engineSQL(t, s, `INSERT INTO teams(name,created_at,updated_at,next_run_number) VALUES ('a',1,1,2)`)
	request, err := json.Marshal(withProvenance(runspec.SealedRequest{Version: 1, RunID: "run", Hash: "request", Request: runspec.Request{Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: "0123456789abcdef0123456789abcdef01234567"}, Source: runspec.Source{Ref: "0123456789abcdef0123456789abcdef01234567"}}}, assertedProvenance(t, "ci", "", "0123456789abcdef0123456789abcdef01234567")))
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,lineage_json,log_retention_class,log_retention_days) VALUES ('a','run',1,'ci','running',?,'request',1,'gh:1','host','execute',?,'account',?,?,'r30',30)`, request, []byte(`[]`), time.Now().UnixNano(), []byte(`{"admission_key":"capability:fixture","admission_subject":{"kind":"account","id":"account"}}`))
	engineSQL(t, s, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES ('a','run','trigger',1,1,?,?,?)`, time.Now().Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	tenant, err := s.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	fixture.EngineController, fixture.Store = NewEngineController(s, EngineController{}), s
	fixture.Server.WithEngineController(fixture.EngineController)
	return fixture, tenant
}

func engineSQL(t *testing.T, s *store.Store, query string, args ...any) {
	t.Helper()
	if _, err := teststore.Exec(t.Context(), s, query, args...); err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(strings.TrimSpace(query))
	if strings.HasPrefix(upper, "INSERT INTO NODES") || strings.HasPrefix(upper, "INSERT INTO TRIGGERS") ||
		strings.HasPrefix(upper, "UPDATE NODES SET") && strings.Contains(upper, "EXECUTION_JSON") ||
		strings.HasPrefix(upper, "UPDATE TRIGGERS SET") && (strings.Contains(upper, "NEEDS_LABELS") || strings.Contains(upper, "UNIT_KIND")) {
		engineIndexClaimFixtures(t, s)
	}
}

// Fixtures rewrite immutable declarations, so each unit owns a private index key.
// Content interning and shared placement holds are exercised through store intake tests.
func engineIndexClaimFixtures(t *testing.T, s *store.Store) {
	t.Helper()
	db := teststore.SQL(t, s)
	rows, err := db.QueryContext(t.Context(), `SELECT team,run_id,node_id,'node',execution_json,NULL FROM nodes UNION ALL SELECT t.team,t.id,'',t.unit_kind,r.spec_json,r.request_json FROM triggers t JOIN runs r ON r.team=t.team AND r.id=t.id`)
	if err != nil {
		t.Fatal(err)
	}
	type unit struct {
		team, run, node, kind string
		terms                 []string
	}
	var units []unit
	for rows.Next() {
		var u unit
		var raw, request []byte
		if err := rows.Scan(&u.team, &u.run, &u.node, &u.kind, &raw, &request); err != nil {
			t.Fatal(err)
		}
		if u.kind == "node" {
			var execution runspec.JobExecutionSettings
			if err := json.Unmarshal(raw, &execution); err != nil {
				t.Fatal(err)
			}
			u.terms = execution.Requires.Value
		} else {
			var sealed runspec.SealedRequest
			if err := json.Unmarshal(request, &sealed); err != nil {
				t.Fatal(err)
			}
			u.terms = sealed.Request.Options.Requires.Value
			var policy struct {
				Resolved struct {
					Settings struct {
						Options *runspec.Options `json:"options"`
					} `json:"settings"`
				} `json:"resolved"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &policy); err != nil {
					t.Fatal(err)
				}
			}
			if policy.Resolved.Settings.Options != nil {
				u.terms = policy.Resolved.Settings.Options.Requires.Value
			}
		}
		units = append(units, u)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		identity, err := json.Marshal([]string{u.kind, u.run, u.node})
		if err != nil {
			t.Fatal(err)
		}
		key, requirements := string(identity), ""
		exec := func(query string, args ...any) {
			t.Helper()
			if _, err := teststore.Exec(t.Context(), s, query, args...); err != nil {
				t.Fatal(err)
			}
		}
		if len(u.terms) > 0 {
			requirements = key
			exec(`INSERT INTO claim_requirements(team,id) VALUES (?,?) ON CONFLICT(team,id) DO NOTHING`, u.team, key)
			exec(`DELETE FROM claim_requirement_terms WHERE team=? AND requirements=?`, u.team, key)
			for term, selector := range u.terms {
				if strings.TrimSpace(selector) == "" {
					continue
				}
				for _, alternative := range strings.Split(selector, ",") {
					exec(`INSERT INTO claim_requirement_terms(team,requirements,term,label) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, u.team, key, term, strings.TrimSpace(alternative))
				}
			}
		}
		exec(`INSERT INTO claim_contexts(team,id,requirements,unit_kind) VALUES (?,?,?,?) ON CONFLICT(team,id) DO UPDATE SET requirements=excluded.requirements`, u.team, key, requirements, u.kind)
		if u.kind == "node" {
			exec(`UPDATE nodes SET claim_requirements=?,claim_context=? WHERE team=? AND run_id=? AND node_id=?`, requirements, key, u.team, u.run, u.node)
		} else {
			exec(`UPDATE triggers SET claim_requirements=?,claim_context=? WHERE team=? AND id=?`, requirements, key, u.team, u.run)
		}
	}
}

func TestEngineCancelRechecksMembershipAndCommitsEffectsOnce(t *testing.T) {
	for _, current := range []bool{true, false} {
		t.Run(map[bool]string{true: "live", false: "removed"}[current], func(t *testing.T) {
			server, tenant := engineRuntimeStore(t)
			s := server.Store
			engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','a@example.test',1,1)`)
			engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','account','editor',1)`)
			engineSQL(t, s, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES ('session','account','runs.read,runs.control',1,?,'a','account')`, time.Now().Add(time.Hour).UnixNano())
			engineSQL(t, s, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES ('session','a',0)`)
			principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "session"})
			if err != nil {
				t.Fatal(err)
			}
			authorized := capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
			if !current {
				engineSQL(t, s, `DELETE FROM memberships WHERE team='a' AND account_id='account'`)
			}
			result, err := server.Cancel(t.Context(), authorized, controllerwire.CancelRequest{Reason: "user_cancelled"})
			if !current {
				var refusal *apierr.Error
				if !errors.As(err, &refusal) || refusal.Code != apierr.Unauthenticated {
					t.Fatalf("removed member cancellation: %+v, %v", result, err)
				}
			} else {
				if err != nil || result.StateSeq != 1 || result.Replayed || !result.CancelRequested {
					t.Fatalf("cancel: %+v, %v", result, err)
				}
				replay, err := server.Cancel(t.Context(), authorized, controllerwire.CancelRequest{Reason: "user_cancelled"})
				if err != nil || !replay.Replayed || replay.StateSeq != result.StateSeq {
					t.Fatalf("replay: %+v, %v", replay, err)
				}
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || run.State != store.RunRunning || (run.CancelRequestedAt != nil) != current {
				t.Fatalf("run state=%s cancellation=%v, %v", run.State, run.CancelRequestedAt, err)
			}
			events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 10)
			if err != nil || current && (len(events) != 1 || events[0].Kind != "run_cancel_requested") || !current && len(events) != 0 {
				t.Fatalf("events: %+v, %v", events, err)
			}
			var effects int
			if err := teststore.SQL(t, s).QueryRow(`SELECT COUNT(*) FROM run_effects WHERE team='a' AND run_id='run'`).Scan(&effects); err != nil || current && effects != 1 || !current && effects != 0 {
				t.Fatalf("effects: %d, %v", effects, err)
			}
		})
	}
}

func engineRuntimeClaim(t *testing.T, server *engineFixture, kind capability.UnitKind) capability.Authorized {
	t.Helper()
	s := server.Store
	digest := sha256.Sum256([]byte("runner-secret"))
	root := capability.Capability{Prefix: "runner", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, ReservedPools: []string{}, Repositories: nil, IssuedAt: time.Unix(0, 1).UTC()}
	envelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: root})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := json.Marshal(root.Grants)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s, `INSERT INTO capabilities(digest,prefix,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,repo_scope,created_at,created_by) VALUES (?,'runner','a','machine','runner','controller',?,'delegate',1,?,'none',?,?,1,'runner')`, hex.EncodeToString(digest[:]), grants, envelope, []byte(`[]`), []byte(`null`))
	target := &store.UnitRef{Kind: kind, RunID: "run"}
	if kind == capability.UnitNode {
		target.NodeID = "job"
		engineSQL(t, s, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: target})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AsOperator().LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	return capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}
}

func TestEngineJobLifecycleRenewsAndReplaysUnderStoreFences(t *testing.T) {
	server, tenant := engineRuntimeStore(t)
	authorized := engineRuntimeClaim(t, server, capability.UnitNode)
	if _, err := server.Renew(t.Context(), authorized, controllerwire.RenewalRequest{Executing: true}); err == nil {
		t.Fatal("executing heartbeat started a job without its transition")
	}
	if _, err := server.Renew(t.Context(), authorized, controllerwire.RenewalRequest{}); err != nil {
		t.Fatal(err)
	}
	engineAdmitExecution(t, server.Store, authorized.Principal)
	started, err := server.TransitionJob(t.Context(), authorized, controllerwire.JobTransitionRequest{To: "running", Cause: "start", SourceAttempt: new(int64(0))})
	if err != nil || started.Attempt != 1 || started.Generation != 1 {
		t.Fatalf("start: %+v, %v", started, err)
	}
	if _, err := server.Renew(t.Context(), authorized, controllerwire.RenewalRequest{Executing: true}); err != nil {
		t.Fatal(err)
	}
	body := controllerwire.JobTransitionRequest{To: "success", Cause: "report", SourceAttempt: &started.Attempt, Result: &controllerwire.JobResult{Outcome: "success"}}
	first, err := server.TransitionJob(t.Context(), authorized, body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.TransitionJob(t.Context(), authorized, body)
	if err != nil || !second.Replayed || second.StateSeq != first.StateSeq {
		t.Fatalf("result replay: %+v, %v", second, err)
	}
	if _, err := server.Renew(t.Context(), authorized, controllerwire.RenewalRequest{}); err == nil {
		t.Fatal("completed claim renewed")
	}
	row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.Outcome != store.NodeSuccess || row.ClaimedBy != "" || row.StateSeq != 3 {
		t.Fatalf("terminal row: %+v, %v", row, err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 10)
	if err != nil || len(events) != 3 || events[0].Kind != "job_claimed" || events[1].Kind != "job_started" || events[2].Kind != "job_succeeded" {
		t.Fatalf("lifecycle events: %+v, %v", events, err)
	}
}

type engineProfileFake struct{}

func (engineProfileFake) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, error) {
	return match.Profile{}, nil
}

func TestEngineOwnerReconcilesExecutesAndFinishes(t *testing.T) {
	server, tenant := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
	server.claimProfiles = engineProfileFake{}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(runspec.Spec{Version: 1, Request: run.Request, Resolved: runspec.Resolved{Guards: runspec.Guards{Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := json.Marshal(nodeproto.JobSpec{ID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.Store, `UPDATE runs SET spec_json=?,spec_hash='spec',sealed_at=1,plan_json=?,plan_hash='plan' WHERE team='a' AND id='run'`, spec, []byte(`{"jobs":[{"id":"job"}]}`))
	engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','job','pending',?,?)`, job, []byte(`{}`))
	if _, err := server.Renew(t.Context(), owner, controllerwire.RenewalRequest{Executing: true}); err != nil {
		t.Fatal(err)
	}
	response, err := server.ReconcileJob(t.Context(), owner, controllerwire.ReconcileRequest{Policy: "none"})
	if err != nil || response.Kind != "execute" || response.Claim == nil || response.Claim.Capability == "" || response.Claim.Accepted == nil || response.Claim.Binding.RunID != "run" || response.Claim.Binding.NodeID != "job" {
		t.Fatalf("self claim: %+v, %v", response, err)
	}
	wait, err := server.ReconcileJob(t.Context(), owner, controllerwire.ReconcileRequest{Policy: "none"})
	if err != nil || wait.Kind != "wait_holder" || wait.Claim != nil {
		t.Fatalf("live holder: %+v, %v", wait, err)
	}
	principal, err := server.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(response.Claim.Capability)))
	if err != nil {
		t.Fatal(err)
	}
	executor := capability.Authorized{Principal: principal, Binding: response.Claim.Binding, Target: owner.Target}
	engineAdmitExecution(t, server.Store, executor.Principal)
	if _, err := server.TransitionJob(t.Context(), executor, controllerwire.JobTransitionRequest{To: "running", Cause: "start", SourceAttempt: new(int64(0))}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.TransitionJob(t.Context(), executor, controllerwire.JobTransitionRequest{To: "success", Cause: "report", SourceAttempt: new(int64(1)), Result: &controllerwire.JobResult{Outcome: "success"}}); err != nil {
		t.Fatal(err)
	}
	terminal, err := server.ReconcileJob(t.Context(), owner, controllerwire.ReconcileRequest{Policy: "resume"})
	if err != nil || terminal.Kind != "keep_terminal" || terminal.Job.Outcome != "success" || terminal.Claim != nil {
		t.Fatalf("adoption: %+v, %v", terminal, err)
	}
	engineSQL(t, server.Store, `CREATE TRIGGER refuse_owner_finish BEFORE UPDATE OF status ON triggers BEGIN SELECT RAISE(ABORT,'owner unavailable'); END`)
	if _, err := server.Finish(t.Context(), owner, controllerwire.FinishRequest{}); err == nil {
		t.Fatal("finish ignored the owner write failure")
	}
	run, err = tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != store.RunRunning || run.FinishedAt != nil {
		t.Fatalf("partial finish: state=%s finished=%v, %v", run.State, run.FinishedAt, err)
	}
	engineSQL(t, server.Store, `DROP TRIGGER refuse_owner_finish`)
	for i := range 2 {
		result, err := server.Finish(t.Context(), owner, controllerwire.FinishRequest{})
		if err != nil || result.Replayed != (i == 1) {
			t.Fatalf("finish %d: %+v, %v", i, result, err)
		}
	}
	run, err = tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != store.RunSuccess || run.FinishedAt == nil {
		t.Fatalf("finished state=%s at %v, %v", run.State, run.FinishedAt, err)
	}
	var status string
	if err := teststore.SQL(t, server.Store).QueryRow(`SELECT status FROM triggers WHERE team='a' AND id='run'`).Scan(&status); err != nil || status != "done" {
		t.Fatalf("owner status=%s, %v", status, err)
	}
}

func engineAcceptedFixturePlan(t *testing.T, e *engineFixture, team store.Team, runID string, jobs ...string) {
	t.Helper()
	tenant, err := e.Store.ForTeam(t.Context(), team)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) == 0 {
		jobs = []string{"job"}
	}
	plan := nodeproto.PlanDoc{Jobs: make([]nodeproto.JobSpec, 0, len(jobs))}
	for _, id := range jobs {
		plan.Jobs = append(plan.Jobs, nodeproto.JobSpec{ID: id})
	}
	rawPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	run.Request.RunID, run.Request.Request.Team = runID, string(team)
	run.Request.Hash, err = runspec.CanonicalHash(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	rawRequest, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: run.Request, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := runspec.SealSpec(runspec.SealInput{Request: run.Request, Declarations: declarations, Plan: rawPlan, Now: time.Unix(0, 1), Resolved: runspec.Resolved{
		Args: run.Request.Request.RequestedArgs, SecretArgs: append([]string{}, run.Request.Request.SecretArgs.Value...), Settings: resolved, Engine: engine.Identity{Version: "v1.2.3"}, Guards: runspec.Guards{Passed: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rawSpec, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET request_json=?,request_hash=?,spec_json=?,spec_hash=?,sealed_at=1,plan_json=?,plan_hash=? WHERE team=? AND id=?`, rawRequest, run.Request.Hash, rawSpec, spec.Hash, rawPlan, spec.PlanHash, team, runID)
	engineSQL(t, e.Store, `UPDATE nodes SET queue_started_at=? WHERE team=? AND run_id=? AND ready_at IS NOT NULL AND queue_started_at IS NULL`, time.Now().UnixNano(), team, runID)
}

func newEngineFixture(st *store.Store, cfg EngineController) *engineFixture {
	return &engineFixture{EngineController: NewEngineController(st, cfg), Store: st}
}

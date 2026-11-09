package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/internal/jwks/jwkstest"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

const (
	fallbackWorkflow = ".github/workflows/fallback.yaml"
	fallbackSHA      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	defaultSHA       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type dispatchFixture struct {
	e      *engineFixture
	tenant *store.Tenant
	app    *githubapptest.GitHub
	now    time.Time
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	f := &dispatchFixture{now: time.Now()}
	f.e, f.tenant = engineRuntimeStore(t, teststore.WithClock(func() time.Time { return f.now }))
	f.app = githubapptest.New(t)
	f.app.AddInstallation(githubapptest.Installation{ID: 7, Account: githubapp.Account{ID: 7, Login: "acme", Type: "Organization"}, Repos: []githubapptest.Repo{{ID: 1, FullName: "acme/repo", Owner: githubapp.Account{ID: 7}, DefaultBranch: "main"}}})
	f.app.SetCommit("acme/repo", "refs/heads/sparkwing-fallback", fallbackSHA)
	f.app.SetFile("acme/repo", fallbackSHA, fallbackWorkflow, []byte("name: fallback\non: workflow_dispatch\n"))
	f.app.SetCommit("acme/repo", "refs/heads/main", defaultSHA)
	f.app.SetFile("acme/repo", defaultSHA, fallbackWorkflow, []byte("name: fallback\non: workflow_dispatch\n"))
	f.app.ActionsRun = githubapp.WorkflowRun{ID: 1000, Attempt: 1, Event: "workflow_dispatch", Path: fallbackWorkflow, SHA: fallbackSHA, Repository: githubapp.Repository{ID: 1, Owner: githubapp.Account{ID: 7}}}
	f.app.ActionsLabels = []string{"ubuntu-latest"}
	f.e.Server.WithGitHubApp(f.app.Config())
	engineSQL(t, f.e.Store, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES(7,'a',7,'acme','Organization',1,1)`)
	engineSQL(t, f.e.Store, `INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,created_at,repo_key,installation_id) VALUES('a',1,7,'acme/repo',1,'gh:1',7)`)
	engineSQL(t, f.e.Store, `INSERT INTO github_app_automation(team,repository_id,repository,installation_id,enabled_by,enabled_at,repo_key) VALUES('a',1,'acme/repo',7,'owner',1,'gh:1')`)
	engineSQL(t, f.e.Store, `INSERT INTO github_actions_approvals(team,repository_id,workflow,runner,workflow_ref,approval_id) VALUES('a',1,?,'ubuntu-latest','refs/heads/sparkwing-fallback','approval')`, fallbackWorkflow)
	run, err := f.tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Dispatch = runspec.DispatchController
	run.Request.Request.Git.Repository.Display = "https://github.com/acme/repo"
	run.Request.Request.Git.Ref = "refs/heads/feature"
	run.Request.Request.Placement = &runspec.RepositoryPlacement{Revision: fallbackSHA, Prefer: []runspec.RepositoryTier{{Target: runspec.PlacementTeam, Hold: 5 * time.Second}, {Target: runspec.PlacementGitHubActions, Workflow: fallbackWorkflow, Runner: "ubuntu-latest"}}}
	run.Request.Provenance, err = runspec.AppProvenance(runspec.AppFacts{Repository: "acme/repo", RepositoryID: 1, Event: "push", Ref: "refs/heads/feature", SHA: run.Request.Request.Git.SHA, DeliveryKey: "delivery", VerifiedAt: f.now}, runspec.Optional[runspec.PullRequest]{}, "ci")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, f.e.Store, `UPDATE runs SET dispatch='controller',request_json=? WHERE team='a' AND id='run'`, raw)
	engineSQL(t, f.e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,queue_started_at,spec_json,execution_json) VALUES('a','run','job','pending',?,?,?,?)`, f.now.UnixNano(), f.now.UnixNano(), []byte(`{"id":"job"}`), []byte(`{}`))
	return f
}

func (f *dispatchFixture) dispatch(t *testing.T) store.ActionsDispatch {
	t.Helper()
	sent, err := f.e.Server.processActionsDispatch(t.Context())
	if err != nil || !sent {
		t.Fatalf("dispatch sent=%v err=%v", sent, err)
	}
	var receipt string
	if err := teststore.SQL(t, f.e.Store).QueryRow(`SELECT receipt FROM github_actions_dispatches ORDER BY queue_started_at DESC,generation DESC LIMIT 1`).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	d, err := f.e.Store.AsOperator().ReadActionsDispatch(t.Context(), "a", receipt)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

//sparkwing:critical
func TestActionsDispatchRequiresDefaultBranchWorkflow(t *testing.T) {
	for _, name := range []string{"identical", "differs", "fallback missing", "default missing", "default unresolved", "truncated"} {
		t.Run(name, func(t *testing.T) {
			f := newDispatchFixture(t)
			switch name {
			case "differs":
				f.app.SetFile("acme/repo", fallbackSHA, fallbackWorkflow, []byte("name: fallback\non: workflow_dispatch\n "))
			case "fallback missing":
				f.app.SetFile("acme/repo", fallbackSHA, fallbackWorkflow, nil)
			case "default missing":
				f.app.SetFile("acme/repo", defaultSHA, fallbackWorkflow, nil)
			case "default unresolved":
				f.app.SetCommit("acme/repo", "refs/heads/main", "")
			case "truncated":
				prefix := strings.Repeat("#", int(limits.ProjectConfigBytes)+1)
				f.app.SetFile("acme/repo", fallbackSHA, fallbackWorkflow, []byte(prefix+"attacker"))
				f.app.SetFile("acme/repo", defaultSHA, fallbackWorkflow, []byte(prefix+"trusted"))
			}
			d := f.dispatch(t)
			if name == "identical" {
				if d.State != "accepted" || d.WorkflowSHA != fallbackSHA || len(f.app.ActionsCalls()) != 1 {
					t.Fatalf("matching workflow not dispatched at verified commit: %+v", d)
				}
				return
			}
			if d.State != "refused" || len(f.app.ActionsCalls()) != 0 || !strings.Contains(d.Reason, fallbackWorkflow) || !strings.Contains(d.Reason, map[string]string{"differs": "sync the fallback", "fallback missing": "add the approved workflow", "default missing": "add the approved workflow", "default unresolved": "check GitHub availability", "truncated": "reduce the workflow file"}[name]) {
				t.Fatalf("unverified workflow dispatched or missing repair hint: %+v", d)
			}
		})
	}
}

//sparkwing:critical
func TestActionsReceiptRechecksVerifiedWorkflow(t *testing.T) {
	for _, name := range []string{"unchanged", "default changed", "default missing", "branch moved", "original run after branch moved", "run changed"} {
		t.Run(name, func(t *testing.T) {
			f := newDispatchFixture(t)
			d := f.dispatch(t)
			claims := f.claims()
			movedSHA := strings.Repeat("c", 40)
			switch name {
			case "default changed":
				f.app.SetCommit("acme/repo", "refs/heads/main", movedSHA)
				f.app.SetFile("acme/repo", movedSHA, fallbackWorkflow, []byte("name: new trusted workflow\n"))
			case "default missing":
				f.app.SetFile("acme/repo", defaultSHA, fallbackWorkflow, nil)
			case "branch moved", "original run after branch moved":
				f.app.SetCommit("acme/repo", claims.Ref, movedSHA)
				f.app.SetFile("acme/repo", movedSHA, fallbackWorkflow, []byte("name: attacker\n"))
				if name == "branch moved" {
					claims.WorkflowSHA, claims.SHA = movedSHA, movedSHA
					f.app.ActionsRun.SHA = movedSHA
				}
			case "run changed":
				f.app.ActionsRun.SHA = movedSHA
			}
			spec, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", d.Receipt, claims)
			if name == "unchanged" || name == "original run after branch moved" {
				if err != nil || spec.Binding.Push == nil || spec.Binding.Push.WorkflowSHA != fallbackSHA {
					t.Fatalf("verified workflow lost its commit binding: %+v %v", spec, err)
				}
			} else if err == nil {
				t.Fatal("unverified workflow received hosted-runner trust")
			}
		})
	}
}

func TestActionsDispatchAtMostOnceAcrossLostResponseAndRestart(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "lost204"}[lost], func(t *testing.T) {
			f := newDispatchFixture(t)
			f.app.LostDispatch = lost
			d := f.dispatch(t)
			want := "accepted"
			if lost {
				want = "uncertain"
			}
			if d.State != want {
				t.Fatalf("state=%s want %s", d.State, want)
			}
			f.e.Server = New(f.e.Store, nil).WithGitHubApp(f.app.Config())
			f.now = f.now.Add(10 * time.Minute)
			for range 3 {
				sent, err := f.e.Server.processActionsDispatch(t.Context())
				if err != nil || sent {
					t.Fatalf("redispatched: %v %v", sent, err)
				}
			}
			if got := len(f.app.ActionsCalls()); got != 1 {
				t.Fatalf("outbound calls=%d", got)
			}
		})
	}
}

func TestActionsDispatchRestartMidSendNeverResends(t *testing.T) {
	f := newDispatchFixture(t)

	started := make(chan string, 1)
	release := make(chan struct{})
	f.app.DispatchStarted = started
	f.app.DispatchContinue = release
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.e.Server.processActionsDispatch(ctx); done <- err }()
	receipt := <-started
	cancel()
	err := <-done
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sender did not stop mid-send: %v", err)
	}
	f.now = f.now.Add(2 * time.Minute)
	var ordinal int
	var database, path string
	if err := teststore.SQL(t, f.e.Store).QueryRow(`PRAGMA database_list`).Scan(&ordinal, &database, &path); err != nil {
		t.Fatal(err)
	}
	if err := f.e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := teststore.OpenExisting(t, path, teststore.WithClock(func() time.Time { return f.now }))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f.e.Store = reopened
	replica := New(reopened, nil).WithGitHubApp(f.app.Config())
	if sent, err := replica.processActionsDispatch(t.Context()); err != nil || sent {
		t.Fatalf("restart sent=%v err=%v", sent, err)
	}
	got, err := f.e.Store.AsOperator().ReadActionsDispatch(t.Context(), "a", receipt)
	if err != nil || got.State != "uncertain" || len(f.app.ActionsCalls()) != 1 {
		t.Fatalf("restart result: %+v %v", got, err)
	}
}

func TestActionsDispatchRefusalsStayVisibleWithoutRedispatch(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newDispatchFixture(t)
			f.app.ActionsStatus = status
			d := f.dispatch(t)
			if d.State != "refused" && d.State != "uncertain" || d.Reason == "" {
				t.Fatalf("invisible failure: %+v", d)
			}
			if sent, err := f.e.Server.processActionsDispatch(t.Context()); err != nil || sent {
				t.Fatalf("retry: %v %v", sent, err)
			}
			if len(f.app.ActionsCalls()) != 1 {
				t.Fatal("multiple dispatches")
			}
		})
	}
}

func TestActionsDispatchTeamClaimWinsDuringHTTP(t *testing.T) {
	f := newDispatchFixture(t)
	engineHTTPRoot(t, f.e, "a", "runner", []capability.Grant{capability.NodesClaim})
	started := make(chan string, 1)
	release := make(chan struct{})
	f.app.DispatchStarted = started
	f.app.DispatchContinue = release
	defer close(release)
	done := make(chan error, 1)
	go func() { _, err := f.e.Server.processActionsDispatch(t.Context()); done <- err }()
	receipt := <-started
	if sent, err := New(f.e.Store, nil).WithGitHubApp(f.app.Config()).processActionsDispatch(t.Context()); err != nil || sent {
		t.Fatalf("second replica sent: %v %v", sent, err)
	}
	claim, err := f.e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Kinds: []capability.UnitKind{capability.UnitNode}})
	if err != nil || claim.NodeID != "job" {
		t.Fatalf("team claim: %+v %v", claim, err)
	}
	assertDispatchHistory(t, f, "sending", receipt, "1 lost race")
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertDispatchHistory(t, f, "1 dispatch", receipt, "accepted", "1 lost race")
	claims := f.claims()
	spec, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", receipt, claims)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := f.e.Store.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if late, err := f.e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("late runner did not exit idle: %+v %v", late, err)
	}
	assertDispatchHistory(t, f, "1 lost race", "https://github.com/acme/repo/actions/runs/1000")
	engineSQL(t, f.e.Store, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES('a','run','job',1,?,?,?,'runner','team','local',?)`, int64(claim.Award.Generation), claim.Award.Holder, claim.Award.CapabilityPrefix, f.now.UnixNano())
	engineSQL(t, f.e.Store, `UPDATE nodes SET status='done',outcome='success',attempt=1,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,ready_at=NULL,queue_started_at=NULL,finished_at=? WHERE node_id='job'`, f.now.UnixNano())
	engineSQL(t, f.e.Store, `UPDATE runs SET status='success',finished_at=? WHERE id='run'`, f.now.UnixNano())
	if err := f.e.Server.processPendingActionsCancellations(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertDispatchHistory(t, f, "cancelled", "1 lost race", receipt, "https://github.com/acme/repo/actions/runs/1000")
}

func (f *dispatchFixture) claims() githuboidc.Claims {
	return githuboidc.Claims{Repository: "acme/repo", RepositoryID: 1, RepositoryOwnerID: 7, Ref: "refs/heads/sparkwing-fallback", SHA: fallbackSHA, WorkflowSHA: fallbackSHA, WorkflowRef: "acme/repo/" + fallbackWorkflow + "@refs/heads/sparkwing-fallback", EventName: "workflow_dispatch", RunID: "1000", RunAttempt: "1", RunnerEnvironment: "github-hosted", CheckRunID: "9000", ExpiresAt: f.now.Add(5 * time.Minute)}
}

func TestActionsReceiptBindsOneJobAndCancelsProvider(t *testing.T) {
	f := newDispatchFixture(t)
	d := f.dispatch(t)
	spec, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", d.Receipt, f.claims())
	if err != nil {
		t.Fatal(err)
	}
	issued, err := f.e.Store.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Store.AsOperator().ProvisionCapability(t.Context(), spec); err == nil {
		t.Fatal("replayed exchange")
	}
	if issued.Capability.Binding.Push.Ref != "refs/heads/feature" || issued.Capability.Binding.Push.SHA == fallbackSHA {
		t.Fatal("dispatcher upgraded workload source")
	}
	engineSQL(t, f.e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES('a','run','other','pending',1,?,?)`, []byte(`{"id":"other"}`), []byte(`{}`))
	if claim, err := f.e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "other"}}); err == nil {
		t.Fatalf("receipt escaped job: %+v", claim)
	}
	claim, err := f.e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}})
	if err != nil || claim.NodeID != "job" {
		t.Fatalf("fallback claim: %+v %v", claim, err)
	}
	engineSQL(t, f.e.Store, `UPDATE runs SET cancel_requested_at=? WHERE id='run'`, f.now.UnixNano())
	if err := f.e.Server.processPendingActionsCancellations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.app.CancelledRuns(); len(got) != 1 || got[0] != "1000" {
		t.Fatalf("provider cancellation: %v", got)
	}
	assertDispatchHistory(t, f, "1 dispatch", "cancelled", d.Receipt, "https://github.com/acme/repo/actions/runs/1000")
}

func TestActionsReceiptRejectsUnboundProviderIdentity(t *testing.T) {
	cases := map[string]func(*githuboidc.Claims){"repository": func(c *githuboidc.Claims) { c.RepositoryID = 99 }, "owner": func(c *githuboidc.Claims) { c.RepositoryOwnerID = 8 }, "workflow": func(c *githuboidc.Claims) {
		c.WorkflowRef = strings.Replace(c.WorkflowRef, "fallback.yaml", "other.yaml", 1)
	}, "revision": func(c *githuboidc.Claims) { c.WorkflowSHA = strings.Repeat("b", 40) }, "attempt": func(c *githuboidc.Claims) { c.RunAttempt = "2" }, "self-hosted": func(c *githuboidc.Claims) { c.RunnerEnvironment = "self-hosted" }, "job": func(c *githuboidc.Claims) { c.CheckRunID = "9001" }, "run": func(c *githuboidc.Claims) { c.RunID = "1001" }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDispatchFixture(t)
			d := f.dispatch(t)
			claims := f.claims()
			change(&claims)
			if _, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", d.Receipt, claims); err == nil {
				t.Fatal("accepted unrelated workflow identity")
			}
		})
	}
	t.Run("spoofed receipt", func(t *testing.T) {
		f := newDispatchFixture(t)
		f.dispatch(t)
		if _, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", "spoof", f.claims()); err == nil {
			t.Fatal("accepted unknown receipt")
		}
	})
}

func dispatchAdmin(t *testing.T, f *dispatchFixture) capability.Authorized {
	t.Helper()
	engineSQL(t, f.e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES('owner','owner@example.test',1,1)`)
	engineSQL(t, f.e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES('a','owner','owner',1)`)
	engineSQL(t, f.e.Store, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES('owner-session','owner','team.admin',1,?,'a','owner')`, f.now.Add(time.Hour).UnixNano())
	engineSQL(t, f.e.Store, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES('owner-session','a',0)`)
	p, err := f.e.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "owner-session"})
	if err != nil {
		t.Fatal(err)
	}
	return capability.Authorized{Principal: p, Target: capability.Target{Team: "a"}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
}

func TestActionsApprovalIsAuditedIdempotentAndFencesOldReceipts(t *testing.T) {
	f := newDispatchFixture(t)
	admin := dispatchAdmin(t, f)
	input := store.ActionsApproval{RepositoryID: 1, Workflow: fallbackWorkflow, Ref: "refs/heads/sparkwing-fallback", Runner: "ubuntu-latest"}
	d := f.dispatch(t)
	if err := f.tenant.PutActionsApproval(t.Context(), admin, input, false); err != nil {
		t.Fatal(err)
	}
	unchanged, err := f.e.Store.AsOperator().ReadActionsDispatch(t.Context(), "a", d.Receipt)
	if err != nil || unchanged.State != "accepted" {
		t.Fatalf("identical approval cancelled dispatch: %+v %v", unchanged, err)
	}
	if err := f.tenant.PutActionsApproval(t.Context(), admin, input, true); err != nil {
		t.Fatal(err)
	}
	if err := f.tenant.PutActionsApproval(t.Context(), admin, input, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", d.Receipt, f.claims()); err == nil {
		t.Fatal("reapproved workflow revived receipt")
	}
	var audits int
	if err := teststore.SQL(t, f.e.Store).QueryRow(`SELECT COUNT(*) FROM audit_events WHERE team='a' AND kind='team.mutated'`).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("approval audits=%d %v", audits, err)
	}
	engineSQL(t, f.e.Store, `UPDATE memberships SET role='editor' WHERE team='a' AND account_id='owner'`)
	if err := f.tenant.PutActionsApproval(t.Context(), admin, input, false); err == nil {
		t.Fatal("non-owner approved workflow")
	}
	engineSQL(t, f.e.Store, `DELETE FROM memberships WHERE team='a' AND account_id='owner'`)
	if err := f.tenant.PutActionsApproval(t.Context(), admin, input, false); err == nil {
		t.Fatal("removed owner changed approval")
	}
}

func TestActionsDispatchOperatorRetryOpensOneNewGeneration(t *testing.T) {
	f := newDispatchFixture(t)
	f.app.LostDispatch = true
	d := f.dispatch(t)
	raw := engineHTTPRoot(t, f.e, "", "operator", []capability.Grant{capability.OperatorLimits})
	p, err := f.e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	authorized := capability.Authorized{Principal: p}
	if err := f.e.Store.AsOperator().RetryActionsDispatch(t.Context(), authorized, "a", d.Receipt, "checked provider state"); err != nil {
		t.Fatal(err)
	}
	old, err := f.e.Store.AsOperator().ReadActionsDispatch(t.Context(), "a", d.Receipt)
	if err != nil || old.State != "cancelled" {
		t.Fatalf("old receipt: %+v %v", old, err)
	}
	f.app.LostDispatch = false
	next := f.dispatch(t)
	if next.Generation != 2 || next.Receipt == d.Receipt || len(f.app.ActionsCalls()) != 2 {
		t.Fatalf("retry did not send one new generation: %+v", next)
	}
	if err := f.e.Store.AsOperator().RetryActionsDispatch(t.Context(), authorized, "a", d.Receipt, "repeated old request"); err == nil {
		t.Fatal("old retry replayed")
	}
	var audits int
	if err := teststore.SQL(t, f.e.Store).QueryRow(`SELECT COUNT(*) FROM audit_events WHERE team='a' AND kind='team.mutated'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("retry audits=%d %v", audits, err)
	}
}

func TestActionsDispatchUnavailableApprovalAndPermission(t *testing.T) {
	t.Run("workflow approval", func(t *testing.T) {
		f := newDispatchFixture(t)
		engineSQL(t, f.e.Store, `DELETE FROM github_actions_approvals`)
		if sent, err := f.e.Server.processActionsDispatch(t.Context()); err != nil || sent {
			t.Fatalf("unapproved dispatch: %v %v", sent, err)
		}
		view, err := f.tenant.ReadRunView(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		if len(view.PlacementWaits) != 1 || !strings.Contains(view.PlacementWaits[0].Tiers[1].Unavailable, "approve") {
			t.Fatalf("approval not visible: %+v", view.PlacementWaits)
		}
	})
	t.Run("App permission", func(t *testing.T) {
		f := newDispatchFixture(t)
		f.app.AddInstallation(githubapptest.Installation{ID: 7, Account: githubapp.Account{ID: 7, Login: "acme", Type: "Organization"}, Repos: []githubapptest.Repo{{ID: 1, FullName: "acme/repo", Owner: githubapp.Account{ID: 7}}}, NoActions: true})
		d := f.dispatch(t)
		if d.State != "refused" || !strings.Contains(d.Reason, "https://github.com/organizations/acme/settings/installations/7") || len(f.app.ActionsCalls()) != 0 {
			t.Fatalf("missing App permission: %+v", d)
		}
	})
	t.Run("missing workflow", func(t *testing.T) {
		f := newDispatchFixture(t)
		f.app.SetFile("acme/repo", fallbackSHA, fallbackWorkflow, nil)
		d := f.dispatch(t)
		if d.State != "refused" || len(f.app.ActionsCalls()) != 0 {
			t.Fatalf("missing workflow: %+v", d)
		}
	})
}

func TestActionsDispatchClaimBeforeIntentStartsNoWorkflow(t *testing.T) {
	f := newDispatchFixture(t)
	engineHTTPRoot(t, f.e, "a", "runner", []capability.Grant{capability.NodesClaim})
	if _, err := f.e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Kinds: []capability.UnitKind{capability.UnitNode}}); err != nil {
		t.Fatal(err)
	}
	if sent, err := f.e.Server.processActionsDispatch(t.Context()); err != nil || sent || len(f.app.ActionsCalls()) != 0 {
		t.Fatalf("claimed job dispatched: %v %v", sent, err)
	}
}

func TestActionsReceiptConsentWithdrawalAndImageMismatch(t *testing.T) {
	t.Run("image", func(t *testing.T) {
		f := newDispatchFixture(t)
		d := f.dispatch(t)
		f.app.ActionsLabels = []string{"windows-latest"}
		if _, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", d.Receipt, f.claims()); err == nil {
			t.Fatal("wrong hosted image accepted")
		}
	})
	t.Run("withdraw", func(t *testing.T) {
		f := newDispatchFixture(t)
		d := f.dispatch(t)
		if err := f.tenant.WithdrawGitHubAppAutomation(t.Context(), 7, 1); err != nil {
			t.Fatal(err)
		}
		engineSQL(t, f.e.Store, `INSERT INTO github_app_automation(team,repository_id,repository,installation_id,enabled_by,enabled_at,repo_key) VALUES('a',1,'acme/repo',7,'owner',2,'gh:1')`)
		if _, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", d.Receipt, f.claims()); err == nil {
			t.Fatal("renewed consent revived receipt")
		}
	})
}

func TestActionsReceiptKeepsTagAndPullRequestSourceThroughOIDC(t *testing.T) {
	for _, event := range []string{"release", "pull_request"} {
		t.Run(event, func(t *testing.T) {
			f := newDispatchFixture(t)
			workload, err := f.tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			ref := "refs/tags/v0.1.0"
			if event == "pull_request" {
				ref = "refs/pull/42/merge"
				workload.Request.Request.PullRequest = runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Number: 42, BaseRepo: "gh:1", HeadRepo: "gh:1", BaseRef: "main", HeadRef: "feature", BaseSHA: fallbackSHA, HeadSHA: workload.Request.Request.Git.SHA}}
			}
			workload.Request.Request.Git.Ref = ref
			workload.Request.Provenance, err = runspec.AppProvenance(runspec.AppFacts{Repository: "acme/repo", RepositoryID: 1, Event: event, Ref: ref, SHA: workload.Request.Request.Git.SHA, DeliveryKey: "event-delivery", VerifiedAt: f.now, DefaultBranch: "main"}, workload.Request.Request.PullRequest, "ci")
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(workload.Request)
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, f.e.Store, `UPDATE runs SET request_json=?`, body)
			d := f.dispatch(t)
			signer := jwkstest.NewSigner(t, "dispatch-key")
			keys := httptest.NewServer(http.HandlerFunc(signer.ServeKeys))
			defer keys.Close()
			f.e.Server.WithGitHubRunners(githuboidc.Config{Audience: "https://controller.test", Issuer: githuboidc.Issuer, JWKSURL: keys.URL, HTTP: keys.Client()})
			token, err := signer.Sign(map[string]any{"iss": githuboidc.Issuer, "aud": "https://controller.test", "exp": f.now.Add(5 * time.Minute).Unix(), "iat": f.now.Unix(), "repository": "acme/repo", "repository_id": "1", "repository_owner_id": "7", "ref": "refs/heads/sparkwing-fallback", "sha": fallbackSHA, "workflow_ref": "acme/repo/" + fallbackWorkflow + "@refs/heads/sparkwing-fallback", "workflow_sha": fallbackSHA, "check_run_id": "9000", "run_id": "1000", "run_attempt": "1", "runner_environment": "github-hosted", "event_name": "workflow_dispatch"})
			if err != nil {
				t.Fatal(err)
			}
			request, err := json.Marshal(githubExchangeReq{IDToken: token, Team: "a", Receipt: d.Receipt})
			if err != nil {
				t.Fatal(err)
			}
			response := engineClaimHTTP(t, f.e.Server.Handler(), "", "POST", "/api/v1/runners/github/exchange", string(request), http.StatusCreated)
			var issued githubExchangeResp
			if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
				t.Fatal(err)
			}
			p, err := f.e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(issued.Token)))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(p.Capability.Grants, []capability.Grant{capability.NodesClaim}) || issued.Binding.Push.Ref != ref || issued.Binding.Push.SHA != workload.Request.Request.Git.SHA {
				t.Fatalf("dispatch broadened original event: %+v", p.Capability)
			}
			engineClaimHTTP(t, f.e.Server.Handler(), "", "POST", "/api/v1/runners/github/exchange", string(request), http.StatusForbidden)
			original, err := f.tenant.ReadRun(t.Context(), "run")
			if err != nil || original.Request.Provenance.DefaultBranchWork() {
				t.Fatalf("dispatcher upgraded original trust: %+v %v", original.Request.Provenance, err)
			}
		})
	}
}

func TestActionsDispatchNewQueueStartAllowsOneNewSend(t *testing.T) {
	f := newDispatchFixture(t)
	old := f.dispatch(t)
	f.now = f.now.Add(time.Second)
	engineSQL(t, f.e.Store, `UPDATE nodes SET queue_started_at=?,ready_at=? WHERE node_id='job'`, f.now.UnixNano(), f.now.UnixNano())
	if err := f.e.Server.processPendingActionsCancellations(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := f.dispatch(t)
	if next.QueueStartedAt == old.QueueStartedAt || len(f.app.ActionsCalls()) != 2 {
		t.Fatalf("new queue generation: %+v", next)
	}
	if sent, err := f.e.Server.processActionsDispatch(t.Context()); err != nil || sent {
		t.Fatalf("new generation resent: %v %v", sent, err)
	}
}

func TestActionsCancellationsHandleEachReceiptAlone(t *testing.T) {
	f := newDispatchFixture(t)
	rows := []struct {
		receipt      string
		installation int64
		run          string
	}{{"gone", 8, "2000"}, {"busy", 7, "1001"}, {"finished", 7, "1002"}, {"live", 7, "1003"}}
	for i, row := range rows {
		engineSQL(t, f.e.Store, `INSERT INTO github_actions_dispatches(receipt,team,run_id,node_id,queue_started_at,generation,approval_id,installation_id,repository_id,repository_owner_id,repository,workflow,workflow_ref,runner,policy_revision,state,provider_run_id) VALUES(?,'a','run','job',1,?,'approval',?,1,7,'acme/repo',?,'refs/heads/sparkwing-fallback','ubuntu-latest','','cancelled',?)`, row.receipt, i+1, row.installation, fallbackWorkflow, row.run)
	}
	f.app.CancelStatus = map[string]int{"1001": http.StatusInternalServerError, "1002": http.StatusConflict}
	if err := f.e.Server.processPendingActionsCancellations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.app.CancelledRuns(); !slices.Equal(got, []string{"1003"}) {
		t.Fatalf("provider cancellations: %v", got)
	}
	for receipt, want := range map[string]int{"gone": 1, "busy": 0, "finished": 1, "live": 1} {
		var sent int
		if err := teststore.SQL(t, f.e.Store).QueryRow(`SELECT cancel_sent FROM github_actions_dispatches WHERE receipt=?`, receipt).Scan(&sent); err != nil || sent != want {
			t.Fatalf("%s cancel_sent=%d want %d: %v", receipt, sent, want, err)
		}
	}
}

func TestActionsReceiptWorkflowSurvivesReturnStartAndFinish(t *testing.T) {
	f := newDispatchFixture(t)
	d := f.dispatch(t)
	f.now = f.now.Add(time.Second)
	engineSQL(t, f.e.Store, `UPDATE nodes SET ready_at=? WHERE node_id='job'`, f.now.UnixNano())
	spec, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", d.Receipt, f.claims())
	if err != nil {
		t.Fatal(err)
	}
	issued, err := f.e.Store.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := f.e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}})
	if err != nil {
		t.Fatal(err)
	}
	// hack: the fixture run has no planned spec, so these writes mirror node_state.go start and report transitions.
	engineSQL(t, f.e.Store, `UPDATE nodes SET status='running',queue_started_at=NULL,attempt=1,started_at=? WHERE node_id='job'`, f.now.UnixNano())
	engineSQL(t, f.e.Store, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES('a','run','job',1,?,?,?,'runner','gh','github',?)`, int64(claim.Award.Generation), claim.Award.Holder, claim.Award.CapabilityPrefix, f.now.UnixNano())
	if err := f.e.Server.processPendingActionsCancellations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if running, err := f.e.Store.AsOperator().ReadActionsDispatch(t.Context(), "a", d.Receipt); err != nil || running.State != "accepted" || len(f.app.CancelledRuns()) != 0 {
		t.Fatalf("running workflow cancelled: %+v %v %v", running, f.app.CancelledRuns(), err)
	}
	assertDispatchHistory(t, f, "0 lost race", d.Receipt, "accepted", "https://github.com/acme/repo/actions/runs/1000")
	engineSQL(t, f.e.Store, `UPDATE nodes SET status='done',outcome='success',finished_at=?,ready_at=NULL,queue_started_at=NULL,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE node_id='job'`, f.now.UnixNano())
	if err := f.e.Server.processPendingActionsCancellations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if finished, err := f.e.Store.AsOperator().ReadActionsDispatch(t.Context(), "a", d.Receipt); err != nil || finished.Reason != "job finished" || len(f.app.CancelledRuns()) != 0 {
		t.Fatalf("finished job cancelled its workflow: %+v %v %v", finished, f.app.CancelledRuns(), err)
	}
	assertDispatchHistory(t, f, "0 lost race", d.Receipt, "job finished", "https://github.com/acme/repo/actions/runs/1000")
	engineSQL(t, f.e.Store, `UPDATE nodes SET status='pending',outcome='',finished_at=NULL,claimed_by='team-runner',capability_prefix='team',lease_expires_at=?,attempt=2 WHERE node_id='job'`, f.now.Add(time.Minute).UnixNano())
	assertDispatchHistory(t, f, "0 lost race", d.Receipt)
}

func TestActionsReceiptCancelledWhenAnotherRunnerRunsTheJob(t *testing.T) {
	f := newDispatchFixture(t)
	d := f.dispatch(t)
	engineSQL(t, f.e.Store, `UPDATE github_actions_dispatches SET provider_run_id='1000',provider_attempt='1' WHERE receipt=?`, d.Receipt)
	engineSQL(t, f.e.Store, `UPDATE nodes SET status='running',queue_started_at=NULL,claimed_by='team-runner',capability_prefix='team',lease_expires_at=?,started_at=?,attempt=1,generation=1 WHERE node_id='job'`, f.now.Add(time.Minute).UnixNano(), f.now.UnixNano())
	if err := f.e.Server.processPendingActionsCancellations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.app.CancelledRuns(); !slices.Equal(got, []string{"1000"}) {
		t.Fatalf("idle fallback workflow kept: %v", got)
	}
}

func TestActionsExchangeReadsWithoutActionsWrite(t *testing.T) {
	f := newDispatchFixture(t)
	d := f.dispatch(t)
	before := len(f.app.Minted())
	if _, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", d.Receipt, f.claims()); err != nil {
		t.Fatal(err)
	}
	readActions := false
	for _, token := range f.app.Minted()[before:] {
		if token.Permissions["actions"] == "write" {
			t.Fatalf("exchange minted Actions write: %v", token.Permissions)
		}
		readActions = readActions || token.Permissions["actions"] == "read"
	}
	if !readActions {
		t.Fatal("exchange did not request Actions read")
	}
}

func TestActionsDispatchSkipsForkPullRequests(t *testing.T) {
	f := newDispatchFixture(t)
	workload, err := f.tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	workload.Request.Request.PullRequest = runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Number: 42, BaseRepo: "gh:1", HeadRepo: "gh:2", BaseRef: "main", HeadRef: "feature", BaseSHA: fallbackSHA, HeadSHA: workload.Request.Request.Git.SHA, Fork: true}}
	body, err := json.Marshal(workload.Request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, f.e.Store, `UPDATE runs SET request_json=?`, body)
	if sent, err := f.e.Server.processActionsDispatch(t.Context()); err != nil || sent || len(f.app.ActionsCalls()) != 0 {
		t.Fatalf("fork pull request dispatched: %v %v", sent, err)
	}
	view, err := f.tenant.ReadRunView(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.PlacementWaits) != 1 || !strings.Contains(view.PlacementWaits[0].Tiers[1].Unavailable, "fork") {
		t.Fatalf("fork refusal not visible: %+v", view.PlacementWaits)
	}
}

func TestActionsFallbackRefMustBeDedicatedBranch(t *testing.T) {
	approve := func(t *testing.T, f *dispatchFixture, ref string) error {
		t.Helper()
		body, err := json.Marshal(store.ActionsApproval{RepositoryID: 1, Workflow: fallbackWorkflow, Ref: ref, Runner: "ubuntu-latest"})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPut, "/api/v1/team/github-app/actions-approval", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		return f.e.Server.handleActionsApproval(httptest.NewRecorder(), r, dispatchAdmin(t, f))
	}
	t.Run("approval", func(t *testing.T) {
		for ref, ok := range map[string]bool{"refs/heads/main": false, "refs/tags/v1.0.0": false, "refs/heads/sparkwing-fallback": true} {
			f := newDispatchFixture(t)
			if err := approve(t, f, ref); (err == nil) != ok {
				t.Fatalf("approve %s: %v", ref, err)
			}
		}
	})
	t.Run("dispatch", func(t *testing.T) {
		f := newDispatchFixture(t)
		engineSQL(t, f.e.Store, `UPDATE github_actions_approvals SET workflow_ref='refs/heads/main'`)
		f.app.SetCommit("acme/repo", "refs/heads/main", fallbackSHA)
		d := f.dispatch(t)
		if d.State != "refused" || !strings.Contains(d.Reason, "dedicated") || len(f.app.ActionsCalls()) != 0 {
			t.Fatalf("default-branch fallback dispatched: %+v", d)
		}
	})
	t.Run("exchange", func(t *testing.T) {
		f := newDispatchFixture(t)
		d := f.dispatch(t)
		f.app.AddInstallation(githubapptest.Installation{ID: 7, Account: githubapp.Account{ID: 7, Login: "acme", Type: "Organization"}, Repos: []githubapptest.Repo{{ID: 1, FullName: "acme/repo", Owner: githubapp.Account{ID: 7}, DefaultBranch: "sparkwing-fallback"}}})
		if _, err := f.e.Server.actionsReceiptProvision(t.Context(), "a", d.Receipt, f.claims()); err == nil {
			t.Fatal("exchange accepted a fallback ref that became the default branch")
		}
	})
}

func TestActionsDispatchSkipsImagesTheJobExcludes(t *testing.T) {
	for requires, dispatched := range map[string]bool{`["os=darwin"]`: false, `["os=linux","arch=arm64"]`: true, `["gpu"]`: false, `["os=darwin,os=linux"]`: true} {
		t.Run(requires, func(t *testing.T) {
			f := newDispatchFixture(t)
			engineSQL(t, f.e.Store, `UPDATE nodes SET execution_json=? WHERE node_id='job'`, []byte(`{"requires":{"set":true,"value":`+requires+`}}`))
			sent, err := f.e.Server.processActionsDispatch(t.Context())
			if err != nil || sent != dispatched || len(f.app.ActionsCalls()) != map[bool]int{false: 0, true: 1}[dispatched] {
				t.Fatalf("dispatch sent=%v err=%v calls=%d", sent, err, len(f.app.ActionsCalls()))
			}
			if dispatched {
				return
			}
			view, err := f.tenant.ReadRunView(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			if len(view.PlacementWaits) != 1 || !strings.Contains(view.PlacementWaits[0].Tiers[1].Unavailable, "ubuntu-latest") {
				t.Fatalf("image refusal not visible: %+v", view.PlacementWaits)
			}
		})
	}
}

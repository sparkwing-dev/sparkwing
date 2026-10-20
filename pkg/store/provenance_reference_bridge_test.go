package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const (
	referenceMain    = "0123456789abcdef0123456789abcdef01234567"
	referenceFeature = "fea7fea7fea7fea7fea7fea7fea7fea7fea7fea7"
	referencePin     = "b1aab1aab1aab1aab1aab1aab1aab1aab1aab1aa"
)

// provenanceObservation is everything a run's sealed identity decides: the OIDC
// subject and commit claim, the data scope it writes and reads, the reserved pools
// it was granted and the team secrets it may select.
type provenanceObservation struct {
	Subject string
	SHA     string
	Write   string
	Reads   []string
	Pools   []string
	Secrets []string
}

// referenceWorld prepares a store in the suite's dialect with the policy every row
// is judged against: a pool only main may use, and a secret scoped to each of two
// pipelines plus one team-wide.
func referenceWorld(t *testing.T, s *Store, repo string) *Tenant {
	t.Helper()
	now := time.Unix(1800000000, 0).UTC()
	if s.dialect == DialectPostgres {
		var err error
		if now, err = s.databaseNow(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	s.clock = func() time.Time { return now }
	for _, team := range []string{"a", "b"} {
		s.testOnlyExec(t, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1) ON CONFLICT(name) DO NOTHING`, team)
		s.testOnlyExec(t, `INSERT INTO team_counters(team) VALUES (?) ON CONFLICT(team) DO NOTHING`, team)
	}
	s.SetIntakePolicy(referencePolicy())
	s.testOnlyExec(t, `INSERT INTO pool_access_rules(team,id,pool,repo_key,branch,source,route,created_by,created_at) VALUES ('a','main-only.000000','deploy-pool',?,'main','','','owner',1)`, repo)
	for _, row := range [][2]string{{"team-token", ""}, {"ci-token", "ci"}, {"deploy-token", "deploy"}} {
		shared := 0
		if row[1] == "" {
			shared = 1
		}
		s.testOnlyExec(t, `INSERT INTO secrets(team,name,value,principal,pipeline,masked,shared,created_at,updated_at) VALUES ('a',?,'value','owner',?,1,?,1,1)`, row[0], row[1], shared)
	}
	return &Tenant{s: s, team: "a"}
}

func referenceMember(t *testing.T, s *Store, id string, role Role) Actor {
	t.Helper()
	s.testOnlyExec(t, "INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?, ?, 1, 1)", id, id+"@example.test")
	s.testOnlyExec(t, "INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a',?,?,1)", id, role)
	s.testOnlyExec(t, "INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,?,?,1,?,'a',?)", id, id, "runs.read,runs.write,runs.control,team.admin", time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano(), id)
	return Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: id}}
}

// referencePolicy answers intake for every entrypoint the reference model drives.
func referencePolicy() IntakePolicy {
	return fakeIntakePolicy(func(ctx context.Context, resolver capability.AuthorityResolver, request IntakeRequest) (IntakePolicyFacts, error) {
		facts := IntakePolicyFacts{RepositoryAuthorized: true, LogRetentionClass: "r30", LogRetentionDays: 30}
		location := request.Request.Source.Location
		facts.SourceLocation = &location
		switch {
		case request.VerifiedDeliveryID != "":
			delivery, err := VerifiedAppIntake(ctx, resolver, request)
			facts.Delivery = &delivery
			return facts, err
		case request.Authority.Kind == "schedule":
			verified, err := VerifiedScheduleIntake(ctx, resolver, request)
			facts.SourceLocation = &verified
			return facts, err
		}
		return facts, nil
	})
}

func observeProvenance(t *testing.T, tenant *Tenant, run string) provenanceObservation {
	t.Helper()
	row, err := tenant.ReadRun(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := oidcIdentity(tenant.team, row)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := oidcissuer.Claims{Team: facts.Team, Pipeline: facts.Pipeline, Trigger: facts.Trigger, RunnerKind: "runner", Ref: facts.Ref, SHA: facts.SHA, RepositoryID: facts.RepositoryID}.Subject()
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(row.Request)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := awardDataScope(awardRow{run: run, repo: string(row.Request.Request.Git.Repository.Key), request: request}, capability.Capability{Unit: capability.UnitNode})
	if err != nil {
		t.Fatal(err)
	}
	out := provenanceObservation{Subject: subject, SHA: facts.SHA, Write: scope.WriteRef, Reads: scope.ReadRefs, Pools: slices.Clone(row.Request.AllowedPools), Secrets: []string{}}
	err = tenant.s.inTx(t.Context(), func(tx *storeTx) error {
		for _, name := range []string{"ci-token", "deploy-token", "team-token"} {
			if _, _, err := selectedSecretTx(t.Context(), tx, tenant.team, name, row.Request.Provenance.Pipeline(), true); err == nil {
				out.Secrets = append(out.Secrets, name)
			} else if err != ErrNotFound {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Pools == nil {
		out.Pools = []string{}
	}
	return out
}

func appReference(t *testing.T, s *Store, ref, sha, pin string) (*Tenant, string) {
	t.Helper()
	tenant := referenceWorld(t, s, "gh:701")
	now := s.now()
	owner := referenceMember(t, s, "owner", RoleOwner)
	installation, err := tenant.TestOnlySeedGitHubInstallation(t.Context(), GitHubAppInstallation{InstallationID: 7, AccountID: 1, AccountLogin: "acme", AccountType: "Organization"}, now)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := tenant.PutAuthorizedGitHubTrigger(t.Context(), teamAuthorization(t, s, owner, "a", "PUT", "/api/v1/team/github-app/triggers"), GitHubAppTrigger{RepositoryID: 701, Repository: "acme/app", InstallationID: 7, Pipeline: "ci", Push: true})
	if err != nil {
		t.Fatal(err)
	}
	digest := "reference-" + strings.ReplaceAll(ref, "/", "-") + pin
	if _, err := s.BindGitHubAppDeliveryEvent(t.Context(), digest, "push", "delivery", now); err != nil {
		t.Fatal(err)
	}
	app := runspec.AppFacts{Event: "push", DeliveryKey: digest, InstallationID: 7, RepositoryID: 701, Ref: ref, SHA: sha, DefaultBranch: "main", DefaultBranchPush: ref == "refs/heads/main", VerifiedAt: now}
	in := GitHubAppAdmission{Installation: installation, Subscription: &sub, Event: "push", Request: runspec.Request{
		Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Origin: runspec.OriginApp, Dispatch: runspec.DispatchHost,
		Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:701", Display: "acme/app"}, Ref: ref, SHA: sha, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: app}},
		Source: runspec.Source{Ref: sha, Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app.git", PipelineDir: ".sparkwing"}},
	}}
	if pin != "" {
		in.Request.Source.RequestedRef, in.Request.Source.Ref = pin, referencePin
	}
	result, err := tenant.AdmitGitHubAppRun(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	return tenant, result.RunID
}

func manualReference(t *testing.T, s *Store, account string) string {
	t.Helper()
	actor := referenceMember(t, s, account, RoleEditor)
	tenant := &Tenant{s: s, team: "a"}
	result, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: actor.Authority, IdempotencyKey: "manual-" + account, Request: runspec.Request{
		Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost,
		Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:701"}, Ref: "refs/heads/main", SHA: referenceFeature},
		Source: runspec.Source{Ref: referenceFeature},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return result.RunID
}

func scheduleReference(t *testing.T, s *Store, mode string) (*Tenant, string) {
	t.Helper()
	tenant := referenceWorld(t, s, "")
	now := s.now()
	sched, _, err := tenant.ArmCronSchedule(t.Context(), CronSchedule{
		ID: "schedule-9", RepoPath: "https://github.com/acme/repo-9.git", GitHubInstallationID: 7, GitHubRepositoryID: 701,
		Pipeline: "nightly", Cron: "* * * * *", TZ: "UTC", Where: CronWhereController,
		GitBranch: "main", LockedRef: referenceMain, ArmedBy: "owner@example.com", Args: map[string]string{"message": "hello"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	s.testOnlyExec(t, `UPDATE pool_access_rules SET repo_key=?`, sched.RepositoryKey)
	s.testOnlyExec(t, `UPDATE cron_schedules SET github_installation_id=7,github_repository_id=701`)
	s.testOnlyExec(t, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',1,'acme','Organization',1,1) ON CONFLICT DO NOTHING`)
	s.testOnlyExec(t, `INSERT INTO github_app_automation(team,repo_key,repository_id,repository,installation_id,enabled_by,enabled_at) VALUES ('a',?,701,'acme/repo',7,'owner',1)`, sched.RepositoryKey)
	if sched, err = tenant.GetCronSchedule(t.Context(), sched.ID); err != nil {
		t.Fatal(err)
	}
	var result AdmissionResult
	switch mode {
	case "fire":
		result, err = tenant.AdmitScheduledRun(t.Context(), sched, now)
	case "override":
		if err := tenant.SetCronOverride(t.Context(), sched.ID, CronOverride{Args: map[string]string{"message": "chosen"}}, now); err != nil {
			t.Fatal(err)
		}
		if sched, err = tenant.GetCronSchedule(t.Context(), sched.ID); err != nil {
			t.Fatal(err)
		}
		result, err = tenant.AdmitScheduledRun(t.Context(), sched, now)
	case "run-now":
		owner := referenceMember(t, s, "owner", RoleOwner)
		_, result.RunID, _, err = tenant.RunAuthorizedSchedule(t.Context(), teamAuthorization(t, s, owner, "a", "PUT", "/api/v1/schedules/repos"), sched.ID, "now")
	}
	if err != nil {
		t.Fatal(err)
	}
	return tenant, result.RunID
}

func actionsReference(t *testing.T, s *Store) (*Tenant, string) {
	t.Helper()
	tenant := referenceWorld(t, s, "gh:701")
	push := authorityRoot("actions-push", "a")
	push.Grants = []capability.Grant{capability.RunsWrite}
	push.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:701", Ref: "refs/heads/main", SHA: referenceMain, WorkflowRunID: "workflow"}}
	authorityPersist(t, s, push)
	result, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: push.Prefix}, IdempotencyKey: "actions", Request: runspec.Request{
		Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost,
		Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:701"}, Ref: "refs/heads/main", SHA: referenceMain},
		Source: runspec.Source{Ref: referenceMain},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, result.RunID
}

// childReference seals the parent run with the given provenance under a live job
// claim, then submits the child request through the real intake with that claim.
func childReference(t *testing.T, s *Store, parent runspec.Provenance, parentGit runspec.Git, child runspec.Request) (*Tenant, string) {
	t.Helper()
	tenant := referenceWorld(t, s, "gh:1")
	now := s.now()
	sealed := runspec.SealedRequest{Version: 1, RunID: "run", Provenance: parent, Request: runspec.Request{Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Git: parentGit, Source: runspec.Source{Ref: parentGit.SHA}}}
	body, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	s.testOnlyExec(t, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_subject_kind,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'ci','running',?,'request',1,'gh:1','host','execute',?,'account','owner',1,'r30',30)`, body, []byte(`[]`))
	s.testOnlyExec(t, `UPDATE teams SET next_run_number=2 WHERE name='a'`)
	s.testOnlyExec(t, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,claimed_by,capability_prefix,generation,attempt,started_at,lease_expires_at) VALUES ('a','run','job','running',?,?,'runner','child-parent',2,3,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`), now.Add(-time.Minute).UnixNano(), now.Add(time.Hour).UnixNano())
	s.testOnlyExec(t, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','job',3,2,'runner','child-parent','runner','runner','remote',?)`, now.Add(-time.Minute).UnixNano())
	expiry := now.Add(time.Hour)
	claim := authorityRoot("child-parent", "a")
	claim.Grants = []capability.Grant{capability.ChildrenStart}
	claim.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "runner", Generation: 2}
	claim.ExpiresAt = &expiry
	authorityPersist(t, s, claim)
	child.Version, child.Team, child.Mode, child.Dispatch = 1, "a", runspec.IntakeExecute, runspec.DispatchHost
	child.Lineage = runspec.Lineage{ParentRunID: "run", ParentJobID: "job"}
	result, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Prefix}, IdempotencyKey: "child", Request: child})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, result.RunID
}

func referenceAppProvenance(t *testing.T, ref, sha string) (runspec.Provenance, runspec.Git) {
	t.Helper()
	app := runspec.AppFacts{Event: "push", DeliveryKey: "parent-delivery", RepositoryID: 1, Ref: ref, SHA: sha, DefaultBranch: "main", DefaultBranchPush: ref == "refs/heads/main", VerifiedAt: time.Unix(1, 0).UTC()}
	p, err := runspec.AppProvenance(app, runspec.Optional[runspec.PullRequest]{}, "ci")
	if err != nil {
		t.Fatal(err)
	}
	return p, runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: ref, SHA: sha, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: app}}
}

func childRequest(pipeline, ref, sha string) runspec.Request {
	return runspec.Request{Pipeline: pipeline, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: ref, SHA: sha}, Source: runspec.Source{Ref: sha}}
}

// ProvenanceReferenceModel is the reference model for sealed provenance: each
// row admits a run through a real store entrypoint, or a child or retry of a sealed
// parent, and states every authority that identity decides. {run} is the admitted
// run's ID.
func ProvenanceReferenceModel(t *testing.T, open func(*testing.T) *Store) {
	asserted := func(pipeline, writer, ref string, secrets ...string) provenanceObservation {
		write := "manual:" + writer + ":" + ref
		return provenanceObservation{Subject: "team:a:repository_id::pipeline:" + pipeline + ":trigger:manual:runner:runner:ref:", Write: write, Reads: []string{write}, Pools: []string{}, Secrets: secrets}
	}
	rows := []struct {
		name  string
		admit func(t *testing.T, s *Store) (*Tenant, string)
		want  provenanceObservation
	}{
		{
			name: "app push to the default branch is verified-default",
			admit: func(t *testing.T, s *Store) (*Tenant, string) {
				return appReference(t, s, "refs/heads/main", referenceMain, "")
			},
			want: provenanceObservation{Subject: "team:a:repository_id:701:pipeline:ci:trigger:push:runner:runner:ref:refs/heads/main", SHA: referenceMain, Write: "refs/heads/main", Reads: []string{"refs/heads/main"}, Pools: []string{"deploy-pool"}, Secrets: []string{"ci-token", "team-token"}},
		},
		{
			name: "app push to a feature branch is verified and reads the default branch",
			admit: func(t *testing.T, s *Store) (*Tenant, string) {
				return appReference(t, s, "refs/heads/feature", referenceFeature, "")
			},
			want: provenanceObservation{Subject: "team:a:repository_id:701:pipeline:ci:trigger:push:runner:runner:ref:refs/heads/feature", SHA: referenceFeature, Write: "refs/heads/feature", Reads: []string{"refs/heads/feature", "refs/heads/main"}, Pools: []string{}, Secrets: []string{"ci-token", "team-token"}},
		},
		{
			name: "a branch source pin on main is demoted to asserted",
			admit: func(t *testing.T, s *Store) (*Tenant, string) {
				return appReference(t, s, "refs/heads/main", referenceMain, "pipelines")
			},
			want: asserted("ci", "run/{run}", "refs/heads/main", "ci-token", "team-token"),
		},
		{
			name: "a full-commit source pin on main keeps verified-default",
			admit: func(t *testing.T, s *Store) (*Tenant, string) {
				return appReference(t, s, "refs/heads/main", referenceMain, referencePin)
			},
			want: provenanceObservation{Subject: "team:a:repository_id:701:pipeline:ci:trigger:push:runner:runner:ref:refs/heads/main", SHA: referenceMain, Write: "refs/heads/main", Reads: []string{"refs/heads/main"}, Pools: []string{"deploy-pool"}, Secrets: []string{"ci-token", "team-token"}},
		},
		{
			name: "a member's manual run naming main is asserted under that member",
			admit: func(t *testing.T, s *Store) (*Tenant, string) {
				return referenceWorld(t, s, "gh:701"), manualReference(t, s, "alice")
			},
			want: asserted("ci", "account/alice", "refs/heads/main", "ci-token", "team-token"),
		},
		{
			name:  "an unmodified schedule fire speaks as cron with its own scope",
			admit: func(t *testing.T, s *Store) (*Tenant, string) { return scheduleReference(t, s, "fire") },
			want:  provenanceObservation{Subject: "team:a:repository_id::pipeline:nightly:trigger:cron:runner:runner:ref:main", SHA: referenceMain, Write: "manual:schedule/schedule-9/owner@example.com:main", Reads: []string{"manual:schedule/schedule-9/owner@example.com:main"}, Pools: []string{}, Secrets: []string{"team-token"}},
		},
		{
			name:  "a schedule fire with overridden arguments is asserted",
			admit: func(t *testing.T, s *Store) (*Tenant, string) { return scheduleReference(t, s, "override") },
			want:  asserted("nightly", "run/{run}", "main", "team-token"),
		},
		{
			name:  "a schedule run-now is asserted by the member who pressed it",
			admit: func(t *testing.T, s *Store) (*Tenant, string) { return scheduleReference(t, s, "run-now") },
			want:  asserted("nightly", "account/owner", "main", "team-token"),
		},
		{
			name:  "a GitHub Actions push keeps its proven ref under its own scope",
			admit: actionsReference,
			want:  provenanceObservation{Subject: "team:a:repository_id::pipeline:ci:trigger:manual:runner:runner:ref:refs/heads/main", SHA: referenceMain, Write: "manual:actions:refs/heads/main", Reads: []string{"manual:actions:refs/heads/main"}, Pools: []string{}, Secrets: []string{"ci-token", "team-token"}},
		},
		{
			name: "a retry by another member copies the App provenance whole",
			admit: func(t *testing.T, s *Store) (*Tenant, string) {
				tenant, source := appReference(t, s, "refs/heads/main", referenceMain, "")
				owner := Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "owner"}}
				result, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: owner.Authority, Request: runspec.Request{Version: 1, Team: "a", Lineage: runspec.Lineage{RetryOf: source}}})
				if err != nil {
					t.Fatal(err)
				}
				return tenant, result.RunID
			},
			want: provenanceObservation{Subject: "team:a:repository_id:701:pipeline:ci:trigger:push:runner:runner:ref:refs/heads/main", SHA: referenceMain, Write: "refs/heads/main", Reads: []string{"refs/heads/main"}, Pools: []string{"deploy-pool"}, Secrets: []string{"ci-token", "team-token"}},
		},
		{
			name: "a child at its parent's revision inherits the parent's identity and pipeline",
			admit: func(t *testing.T, s *Store) (*Tenant, string) {
				parent, git := referenceAppProvenance(t, "refs/heads/main", referenceMain)
				return childReference(t, s, parent, git, childRequest("deploy", "refs/heads/main", referenceMain))
			},
			want: provenanceObservation{Subject: "team:a:repository_id:1:pipeline:ci:trigger:push:runner:runner:ref:refs/heads/main", SHA: referenceMain, Write: "refs/heads/main", Reads: []string{"refs/heads/main"}, Pools: []string{"deploy-pool"}, Secrets: []string{"ci-token", "team-token"}},
		},
		{
			// 10#1 and 13 C1: a feature-branch job names main and the deploy pipeline
			// with its own commit. It must not mint main's deploy subject, reach the
			// deploy pipeline's secrets, the main-only pool or main's cache.
			name: "attack: a feature child claiming main's ref and the deploy pipeline is refused main's identity",
			admit: func(t *testing.T, s *Store) (*Tenant, string) {
				parent, git := referenceAppProvenance(t, "refs/heads/feature", referenceFeature)
				return childReference(t, s, parent, git, childRequest("deploy", "refs/heads/main", referenceFeature))
			},
			want: asserted("ci", "run/run", "refs/heads/main", "ci-token", "team-token"),
		},
		{
			name: "a child of main that resolves another branch is asserted under its parent run",
			admit: func(t *testing.T, s *Store) (*Tenant, string) {
				parent, git := referenceAppProvenance(t, "refs/heads/main", referenceMain)
				return childReference(t, s, parent, git, childRequest("deploy", "refs/heads/feature", referenceFeature))
			},
			want: asserted("ci", "run/run", "refs/heads/feature", "ci-token", "team-token"),
		},
		{
			name: "a child of an asserted run keeps its parent's writer",
			admit: func(t *testing.T, s *Store) (*Tenant, string) {
				parent, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "alice"), "refs/heads/main", referenceMain, "ci")
				if err != nil {
					t.Fatal(err)
				}
				git := runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "refs/heads/main", SHA: referenceMain}
				return childReference(t, s, parent, git, childRequest("deploy", "refs/heads/feature", referenceFeature))
			},
			want: asserted("ci", "account/alice", "refs/heads/feature", "ci-token", "team-token"),
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			tenant, run := row.admit(t, open(t))
			got := observeProvenance(t, tenant, run)
			want := row.want
			want.Write = strings.ReplaceAll(want.Write, "{run}", run)
			want.Reads = slices.Clone(want.Reads)
			for i := range want.Reads {
				want.Reads[i] = strings.ReplaceAll(want.Reads[i], "{run}", run)
			}
			if want.Subject != got.Subject || want.SHA != got.SHA || want.Write != got.Write || !slices.Equal(want.Reads, got.Reads) || !slices.Equal(want.Pools, got.Pools) || !slices.Equal(want.Secrets, got.Secrets) {
				t.Fatalf("observed %+v\nwant     %+v", got, want)
			}
		})
	}
}

// UnverifiedWritersShareNoScope is 11 S3 and 13 C2: one
// editor's manual run naming main writes a scope that no other member's run, no
// schedule fire, no Actions build and no verified build of main reads.
func UnverifiedWritersShareNoScope(t *testing.T, open func(*testing.T) *Store) {
	s := open(t)
	tenant := referenceWorld(t, s, "gh:701")
	alice := observeProvenance(t, tenant, manualReference(t, s, "alice"))
	readers := map[string]provenanceObservation{"bob": observeProvenance(t, tenant, manualReference(t, s, "bob"))}
	for name, admit := range map[string]func(*testing.T, *Store) (*Tenant, string){
		"schedule": func(t *testing.T, s *Store) (*Tenant, string) { return scheduleReference(t, s, "fire") },
		"actions":  actionsReference,
		"app main": func(t *testing.T, s *Store) (*Tenant, string) {
			return appReference(t, s, "refs/heads/main", referenceMain, "")
		},
	} {
		other, run := admit(t, open(t))
		readers[name] = observeProvenance(t, other, run)
	}
	for name, reader := range readers {
		if slices.Contains(reader.Reads, alice.Write) || reader.Write == alice.Write {
			t.Fatalf("%s reads or writes alice's scope %q: %+v", name, alice.Write, reader)
		}
	}
}

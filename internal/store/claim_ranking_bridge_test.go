package store

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

const rankingSHA = "0123456789abcdef0123456789abcdef01234567"

// RankingFixture is one run-anywhere run with a plan unit, a ready job and one claimant per tier.
type RankingFixture struct {
	Store                                             *Store
	Self, Personal, Tablet, Actions, Launcher, Second string
	Owner, Admin, Member                              runstate.Actor
	ReadyAt                                           time.Time
	now                                               time.Time
}

// TestOnlyRankingFixture seals a run-anywhere run whose plan unit and job become ready at ReadyAt.
func (s *Store) TestOnlyRankingFixture(t *testing.T, funded bool) *RankingFixture {
	t.Helper()
	for _, team := range []string{"a", "b"} {
		s.testOnlyExec(t, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1) ON CONFLICT(name) DO NOTHING`, team)
	}
	_, demand := unitClaimFixtureStore(t, s)
	f := &RankingFixture{Store: s, Self: demand.CapabilityPrefix, ReadyAt: s.now()}
	f.RequireOnJobs(t)
	// Claimants must exist before the polls that establish availability at readiness.
	f.now = f.ReadyAt.Add(-time.Minute)
	s.pinnedClock = func() time.Time { return f.now }
	t.Cleanup(func() { s.pinnedClock = nil })
	f.Owner = rankingMember(t, s, "owner", RoleOwner)
	f.Admin = rankingMember(t, s, "admin", RoleOwner)
	f.Member = rankingMember(t, s, "member", RoleEditor)
	f.Personal = f.personalMachine(t, f.Owner, "laptop")
	f.Tablet = f.personalMachine(t, f.Owner, "tablet")
	f.Second = f.personalMachine(t, f.Member, "desktop")
	actions := authorityRoot("actions", "a")
	actions.Grants = []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
	actions.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Repository: "acme/repo", Ref: "refs/heads/main", SHA: rankingSHA, WorkflowRunID: "77", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}}
	authorityPersist(t, s, actions)
	f.Actions = actions.Prefix
	launcher := authorityRoot("launcher", "")
	launcher.Subject.Kind = capability.SubjectService
	launcher.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, s, launcher)
	f.Launcher = launcher.Prefix
	if funded {
		fundUnitClaim(t, s)
	}
	f.At(0)
	f.Request(t, func(*runspec.Request) {})
	s.testOnlyExec(t, `UPDATE runs SET dispatch='controller',created_principal='owner',created_subject_kind='account',git_branch='refs/heads/main',git_sha=?`, rankingSHA)
	s.testOnlyExec(t, `UPDATE triggers SET unit_kind='plan',available_at=?`, f.ReadyAt.UnixNano())
	s.testOnlyExec(t, `UPDATE nodes SET ready_at=?`, f.ReadyAt.UnixNano())
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitPlan, RunID: "run"})
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
	return f
}

func rankingMember(t *testing.T, s *Store, id string, role Role) runstate.Actor {
	t.Helper()
	s.testOnlyExec(t, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?,?,1,1)`, id, id+"@example.test")
	s.testOnlyExec(t, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a',?,?,1)`, id, role)
	s.testOnlyExec(t, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,?,?,1,?,'a',?)`, id, id, "runs.read,runs.write,runs.control,team.admin", time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano(), id)
	s.testOnlyExec(t, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES (?,'a',0)`, id)
	return runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: id}}
}

func (f *RankingFixture) personalMachine(t *testing.T, owner runstate.Actor, name string) string {
	t.Helper()
	issued, err := f.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &owner.Authority, RegistrationID: "runner:a:" + name, Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: name}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Capability.Prefix
}

// SelfHosted registers another shared machine claimant.
func (f *RankingFixture) SelfHosted(t *testing.T, name string) string {
	t.Helper()
	root := authorityRoot(name, "a")
	root.Digest = sha256.Sum256([]byte("ranking-" + name))
	root.Grants = []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
	authorityPersist(t, f.Store, root)
	return root.Prefix
}

// At moves the store clock to ReadyAt plus offset.
func (f *RankingFixture) At(offset time.Duration) { f.now = f.ReadyAt.Add(offset) }

// Request rewrites the run's sealed request.
func (f *RankingFixture) Request(t *testing.T, mutate func(*runspec.Request)) {
	t.Helper()
	request := runspec.Request{Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchController, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}, Ref: "refs/heads/main", SHA: rankingSHA}}
	mutate(&request)
	provenance, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "owner"), request.Git.Ref, request.Git.SHA, request.Pipeline)
	switch request.Origin {
	case runspec.OriginApp:
		provenance, err = runspec.AppProvenance(runspec.AppFacts{Repository: "acme/repo", Event: "push", RepositoryID: 1, DeliveryKey: "delivery", VerifiedAt: f.now, Ref: request.Git.Ref, SHA: request.Git.SHA}, runspec.Optional[runspec.PullRequest]{}, request.Pipeline)
	case runspec.OriginSchedule:
		provenance, err = runspec.ScheduleProvenance("schedule", 1, "acme/repo", "main", request.Git.SHA, request.Pipeline)
	}
	if err != nil {
		t.Fatal(err)
	}
	body := sealWith(t, runspec.SealedRequest{Request: request}, provenance)
	f.Store.testOnlyExec(t, `UPDATE runs SET request_json=?,dispatch=?,trigger_source=?`, body, string(request.Dispatch), string(request.Origin))
	needs, err := json.Marshal(request.Options.Requires.Value)
	if err != nil {
		t.Fatal(err)
	}
	f.Store.testOnlyExec(t, `UPDATE triggers SET needs_labels=? WHERE team='a' AND id='run'`, needs)
	indexFixtureUnit(t, f.Store, "a", UnitRef{Kind: capability.UnitPlan, RunID: "run"})
	indexFixtureUnit(t, f.Store, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
}

// Poll records a claimant offering one free slot at the current clock.
func (f *RankingFixture) Poll(t *testing.T, prefix string, kinds ...capability.UnitKind) {
	t.Helper()
	if err := f.Store.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: prefix, Kinds: kinds, Labels: []string{"team-4cpu"}}); err != nil {
		t.Fatal(err)
	}
}

// Claim polls and then claims from the ordered queue for one unit kind.
func (f *RankingFixture) Claim(t *testing.T, prefix string, kind capability.UnitKind) (UnitClaim, error) {
	t.Helper()
	f.Poll(t, prefix, kind)
	return f.Store.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: prefix, Kinds: []capability.UnitKind{kind}, Labels: []string{"team-4cpu"}})
}

// Rank changes the team's claim ranking as the given member.
func (f *RankingFixture) Rank(t *testing.T, actor runstate.Actor, tiers ...string) error {
	t.Helper()
	p, err := f.Store.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		return err
	}
	route, _ := capability.LookupRoute("PUT", "/api/v1/team/settings")
	a, err := capability.Authorize(t.Context(), f.Store.AsOperator(), route, capability.Authenticated{Principal: p, Method: "PUT", Pattern: "/api/v1/team/settings", Live: []capability.LiveAccess{{RuleIndex: 0}}}, capability.Target{Team: "a"})
	if err != nil {
		return err
	}
	return (&Tenant{s: f.Store, team: "a"}).SetAuthorizedTeamSettings(t.Context(), a, TeamSettingsUpdate{ClaimRanking: &tiers})
}

// AddJob inserts another ready job of the fixture run.
func (f *RankingFixture) AddJob(t *testing.T, id string, readyAt time.Time) {
	t.Helper()
	f.Store.testOnlyExec(t, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,claim_requirements,claim_context) SELECT team,run_id,?,status,?,spec_json,execution_json,claim_requirements,claim_context FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`, id, readyAt.UnixNano())
}

// RequireOnJobs sets every node's resolved requirements to terms.
func (f *RankingFixture) RequireOnJobs(t *testing.T, terms ...string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"requires": map[string]any{"set": true, "value": terms}})
	if err != nil {
		t.Fatal(err)
	}
	f.Store.testOnlyExec(t, `UPDATE nodes SET execution_json=?`, body)
	indexFixtureUnit(t, f.Store, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
}

// ClaimKinds polls and then claims from the ordered queue for any of the unit kinds.
func (f *RankingFixture) ClaimKinds(t *testing.T, prefix string, kinds ...capability.UnitKind) (UnitClaim, error) {
	t.Helper()
	f.Poll(t, prefix, kinds...)
	return f.Store.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: prefix, Kinds: kinds, Labels: []string{"team-4cpu"}})
}

// LocalQueue registers a local controller queue claimant of the team.
func (f *RankingFixture) LocalQueue(t *testing.T) string {
	t.Helper()
	root := authorityRoot("local-queue", "a")
	root.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
	root.Grants = []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
	authorityPersist(t, f.Store, root)
	return root.Prefix
}

// PollWithSlots records the last advertised free-slot count.
func (f *RankingFixture) PollWithSlots(t *testing.T, prefix string, slots int) {
	t.Helper()
	if err := f.Store.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: prefix, Labels: []string{"team-4cpu"}, FreeSlots: &slots}); err != nil {
		t.Fatal(err)
	}
}

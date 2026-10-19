package store

import (
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

const rankingSHA = "0123456789abcdef0123456789abcdef01234567"

// RankingFixture is one run-anywhere run with a plan unit, a ready job and one claimant per tier.
type RankingFixture struct {
	Store                                             *Store
	Self, Personal, Tablet, Actions, Launcher, Second string
	Owner, Admin, Member                              Actor
	ReadyAt                                           time.Time
	now                                               time.Time
}

// TestOnlyRankingFixture seals a run-anywhere run whose plan unit and job become ready at ReadyAt.
func (s *Store) TestOnlyRankingFixture(t *testing.T, funded bool) *RankingFixture {
	t.Helper()
	for _, team := range []string{"a", "b"} {
		s.testOnlyExec(t, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1) ON CONFLICT(name) DO NOTHING`, team)
		s.testOnlyExec(t, `INSERT INTO team_counters(team) VALUES (?) ON CONFLICT(team) DO NOTHING`, team)
	}
	_, demand := unitClaimFixtureStore(t, s)
	f := &RankingFixture{Store: s, Self: demand.CapabilityPrefix, ReadyAt: s.now()}
	f.now = f.ReadyAt
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
	actions.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Ref: "refs/heads/main", SHA: rankingSHA, WorkflowRunID: "77"}}
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
	f.Request(t, func(*runspec.Request) {})
	s.testOnlyExec(t, `UPDATE runs SET dispatch='controller',created_principal='owner',created_subject_kind='account',git_branch='refs/heads/main',git_sha=?`, rankingSHA)
	s.testOnlyExec(t, `UPDATE triggers SET unit_kind='plan',available_at=?`, f.ReadyAt.UnixNano())
	s.testOnlyExec(t, `UPDATE nodes SET ready_at=?`, f.ReadyAt.UnixNano())
	return f
}

func rankingMember(t *testing.T, s *Store, id string, role Role) Actor {
	t.Helper()
	s.testOnlyExec(t, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?,?,1,1)`, id, id+"@example.test")
	s.testOnlyExec(t, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a',?,?,1)`, id, role)
	s.testOnlyExec(t, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,?,?,1,?,'a',?)`, id, id, "runs.read,runs.write,runs.control,team.admin", time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano(), id)
	return Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: id}}
}

func (f *RankingFixture) personalMachine(t *testing.T, owner Actor, name string) string {
	t.Helper()
	issued, err := f.Store.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &owner.Authority, RegistrationID: "runner:a:" + name, Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: name}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
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
	body, err := json.Marshal(runspec.SealedRequest{Request: request})
	if err != nil {
		t.Fatal(err)
	}
	f.Store.testOnlyExec(t, `UPDATE runs SET request_json=?,dispatch=?,trigger_source=?`, body, string(request.Dispatch), string(request.Origin))
}

// Poll records an ordered-queue ask from the claimant at the current clock.
func (f *RankingFixture) Poll(t *testing.T, prefix string, kinds ...capability.UnitKind) {
	t.Helper()
	if err := f.Store.RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: prefix, Kinds: kinds, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}); err != nil {
		t.Fatal(err)
	}
}

// Claim polls and then asks the ordered queue for one unit kind.
func (f *RankingFixture) Claim(t *testing.T, prefix string, kind capability.UnitKind) (UnitClaim, error) {
	t.Helper()
	f.Poll(t, prefix, kind)
	return f.Store.ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: prefix, Kinds: []capability.UnitKind{kind}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}})
}

// Rank changes the team's claim ranking as the given member.
func (f *RankingFixture) Rank(t *testing.T, actor Actor, tiers ...string) error {
	t.Helper()
	p, err := f.Store.ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		return err
	}
	route, _ := capability.LookupRoute("PUT", "/api/v1/team/settings")
	a, err := capability.Authorize(t.Context(), f.Store, route, capability.Authenticated{Principal: p, Method: "PUT", Pattern: "/api/v1/team/settings", Live: []capability.LiveAccess{{RuleIndex: 0}}}, capability.Target{Team: "a"})
	if err != nil {
		return err
	}
	return (&Tenant{s: f.Store, team: "a"}).SetAuthorizedTeamSettings(t.Context(), a, controllerwire.TeamSettingsUpdate{ClaimRanking: &tiers})
}

// AddJob inserts another ready job of the fixture run.
func (f *RankingFixture) AddJob(t *testing.T, id string, readyAt time.Time) {
	t.Helper()
	f.Store.testOnlyExec(t, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,?,status,?,spec_json,execution_json FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`, id, readyAt.UnixNano())
}

// Approve has the owner request automated runs for the machine and the admin approve them.
func (f *RankingFixture) Approve(t *testing.T, machine string) {
	t.Helper()
	tenant := &Tenant{s: f.Store, team: "a"}
	if _, err := tenant.RequestMachineAutomation(t.Context(), f.Owner.Authority, machine, true); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.ApproveMachineAutomation(t.Context(), teamAuthorization(t, f.Store, f.Admin, "a", "PUT", "/api/v1/runners/{runner}/automation/approval"), machine, true); err != nil {
		t.Fatal(err)
	}
}

// AdminAuthorization authorizes the approval route as the given member.
func (f *RankingFixture) AdminAuthorization(t *testing.T, actor Actor) (capability.Authorized, error) {
	t.Helper()
	p, err := f.Store.ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		return capability.Authorized{}, err
	}
	route, _ := capability.LookupRoute("PUT", "/api/v1/runners/{runner}/automation/approval")
	return capability.Authorize(t.Context(), f.Store, route, capability.Authenticated{Principal: p, Method: "PUT", Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, capability.Target{Team: "a"})
}

// LinkGitHub links the account to a GitHub user ID.
func (f *RankingFixture) LinkGitHub(t *testing.T, account string, id int64) {
	t.Helper()
	f.Store.testOnlyExec(t, `INSERT INTO identities(provider,subject,account_id,created_at) VALUES (?,?,?,1)`, ProviderGitHub, strconv.FormatInt(id, 10), account)
}

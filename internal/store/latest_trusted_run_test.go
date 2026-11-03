package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const (
	trustedRepo      = runspec.RepoKey("gh:1")
	trustedOtherRepo = runspec.RepoKey("gh:2")
	trustedSHA       = "89abcdef0123456789abcdef0123456789abcdef"
)

var trustedMachine = capability.Subject{Kind: capability.SubjectMachine, ID: "workstation"}

type trustedSeed struct {
	repo      runspec.RepoKey
	sha       string
	proof     func(t *testing.T) runspec.Provenance
	publisher *capability.Subject
	age       time.Duration
}

func appProof(id int64, sha string) func(t *testing.T) runspec.Provenance {
	return func(t *testing.T) runspec.Provenance {
		p, err := runspec.AppProvenance(runspec.AppFacts{
			Repository: "acme/repo", Event: "push", RepositoryID: id, DeliveryKey: "delivery-" + sha[:8] + string(rune('a'+id)),
			VerifiedAt: time.Unix(1700000000, 0), Ref: "refs/heads/main", SHA: sha, DefaultBranch: "main", DefaultBranchPush: true,
		}, runspec.Optional[runspec.PullRequest]{}, "gate")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
}

func fetchedProof(id int64, sha string) func(t *testing.T) runspec.Provenance {
	return func(t *testing.T) runspec.Provenance {
		ref := "refs/heads/agent"
		if sha == "" {
			ref = ""
		}
		p, err := assertedProvenance(t, "gate", "refs/heads/agent", trustedSHA).FetchedFrom(id, "acme/repo", ref, sha)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
}

func assertedProof(sha string) func(t *testing.T) runspec.Provenance {
	return func(t *testing.T) runspec.Provenance { return assertedProvenance(t, "gate", "refs/heads/agent", sha) }
}

func seedTrustedRun(t *testing.T, tenant *Tenant, request IntakeRequest, pipeline string, seed trustedSeed) string {
	t.Helper()
	id := seedLatestRun(t, tenant, request, pipeline, "success", seed.age+time.Minute, seed.age)
	row, err := tenant.ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	row.Request.Request.Git.Repository.Key = seed.repo
	row.Request.Request.Git.SHA = seed.sha
	mode, kind, principal := "execute", "account", "fixture"
	if seed.publisher != nil {
		mode, kind, principal = "publish", string(seed.publisher.Kind), seed.publisher.ID
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=?,repo_key=?,git_sha=?,intake_mode=?,created_subject_kind=?,created_principal=? WHERE team=? AND id=?`,
		sealWith(t, row.Request, seed.proof(t)), string(seed.repo), seed.sha, mode, kind, principal, tenant.team, id)
	return id
}

func TestTrustedRunMatchesOnlyProvenOrDesignatedRunsAtTheReadersCommit(t *testing.T) {
	designated := TrustedRunners{trustedRepo: {trustedMachine}}
	stranger := capability.Subject{Kind: capability.SubjectMachine, ID: "stranger"}
	for name, tc := range map[string]struct {
		candidate trustedSeed
		trusted   TrustedRunners
		want      bool
	}{
		"proven by an App delivery":                {candidate: trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: appProof(1, trustedSHA)}, want: true},
		"proven by the controller's own fetch":     {candidate: trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: fetchedProof(1, trustedSHA)}, want: true},
		"published by a designated runner":         {candidate: trustedSeed{repo: "local:" + runspec.RepoKey(trustedSHA+trustedSHA[:24]), sha: trustedSHA, proof: assertedProof(trustedSHA), publisher: &trustedMachine}, trusted: designated, want: true},
		"asserted with no designation":             {candidate: trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: assertedProof(trustedSHA)}, trusted: designated},
		"published by an untrusted machine":        {candidate: trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: assertedProof(trustedSHA), publisher: &stranger}, trusted: designated},
		"published by a runner with no config":     {candidate: trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: assertedProof(trustedSHA), publisher: &trustedMachine}},
		"designated only for another repository":   {candidate: trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: assertedProof(trustedSHA), publisher: &trustedMachine}, trusted: TrustedRunners{trustedOtherRepo: {trustedMachine}}},
		"proven for another repository":            {candidate: trustedSeed{repo: trustedOtherRepo, sha: trustedSHA, proof: appProof(2, trustedSHA)}, trusted: designated},
		"proven at another commit":                 {candidate: trustedSeed{repo: trustedRepo, sha: fixtureOtherSHA, proof: appProof(1, fixtureOtherSHA)}, trusted: designated},
		"designated runner at another commit":      {candidate: trustedSeed{repo: trustedRepo, sha: fixtureOtherSHA, proof: assertedProof(fixtureOtherSHA), publisher: &trustedMachine}, trusted: designated},
		"fetched with no proven commit":            {candidate: trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: fetchedProof(1, "")}, trusted: designated},
		"executed, not published, on that machine": {candidate: trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: assertedProof(trustedSHA)}, trusted: TrustedRunners{trustedRepo: {{Kind: capability.SubjectAccount, ID: "fixture"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			tenant, request, _ := intakeFixture(t)
			candidate := seedTrustedRun(t, tenant, request, "gate", tc.candidate)
			reader := seedTrustedRun(t, tenant, request, "main-ci", trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: appProof(1, trustedSHA)})
			tenant.s.SetTrustedRunners(tc.trusted)
			got, err := tenant.GetLatestTrustedRun(t.Context(), "gate", reader, []string{"success"}, 0)
			if tc.want && (err != nil || got.ID != candidate) {
				t.Fatalf("want %s, got %+v %v", candidate, got, err)
			}
			if !tc.want && !errors.Is(err, ErrNotFound) {
				t.Fatalf("want no trusted run, got %+v %v", got, err)
			}
		})
	}
}

func TestTrustedRunNeverCrossesTeams(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	reader := seedTrustedRun(t, tenant, request, "main-ci", trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: appProof(1, trustedSHA)})
	root := authorityRoot("other", "b")
	root.Grants = []capability.Grant{capability.RunsWrite}
	authorityPersist(t, tenant.s, root)
	other := &Tenant{s: tenant.s, team: "b"}
	request.Authority.ID, request.Request.Team = "other", "b"
	seedTrustedRun(t, other, request, "gate", trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: appProof(1, trustedSHA)})
	seedTrustedRun(t, other, request, "gate", trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: assertedProof(trustedSHA), publisher: &trustedMachine})
	tenant.s.SetTrustedRunners(TrustedRunners{trustedRepo: {trustedMachine}})
	if got, err := tenant.GetLatestTrustedRun(t.Context(), "gate", reader, []string{"success"}, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another team's runs satisfied the reader: %+v %v", got, err)
	}
	if _, err := tenant.GetLatestTrustedRun(t.Context(), "gate", "run-elsewhere", nil, 0); err == nil {
		t.Fatal("a reader outside the team was accepted")
	}
}

func TestAnAssertedReaderMatchesOnlyThroughADesignatedPublisher(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	seedTrustedRun(t, tenant, request, "gate", trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: appProof(1, trustedSHA)})
	reader := seedTrustedRun(t, tenant, request, "main-ci", trustedSeed{repo: trustedRepo, sha: trustedSHA, proof: assertedProof(trustedSHA)})
	if got, err := tenant.GetLatestTrustedRun(t.Context(), "gate", reader, []string{"success"}, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an asserted reader borrowed a proven repository: %+v %v", got, err)
	}
}

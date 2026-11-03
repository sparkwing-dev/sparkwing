package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

const (
	provenRepo  = runspec.RepoKey("gh:1")
	provenOther = runspec.RepoKey("gh:2")
	provenSHA   = "89abcdef0123456789abcdef0123456789abcdef"
	otherSHA    = "0123456789abcdef0123456789abcdef01234567"
	provenJob   = "record"
)

type provenSeed struct {
	repo    runspec.RepoKey
	sha     string
	proof   func(t *testing.T) runspec.Provenance
	options runspec.Options
	env     []string
}

func appProof(id int64, sha string, defaultBranch bool) func(t *testing.T) runspec.Provenance {
	return func(t *testing.T) runspec.Provenance {
		ref := "refs/heads/agent"
		if defaultBranch {
			ref = "refs/heads/main"
		}
		p, err := runspec.AppProvenance(runspec.AppFacts{
			Repository: "acme/repo", Event: "push", RepositoryID: id, DeliveryKey: fmt.Sprintf("delivery-%d-%s-%v", id, sha[:8], defaultBranch),
			VerifiedAt: time.Unix(1700000000, 0), Ref: ref, SHA: sha, DefaultBranch: "main", DefaultBranchPush: defaultBranch,
		}, runspec.Optional[runspec.PullRequest]{}, "gate")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
}

func assertedProof(sha string) func(t *testing.T) runspec.Provenance {
	return func(t *testing.T) runspec.Provenance {
		p, err := runspec.AssertedProvenance(runspec.RunWriter("fixture"), "refs/heads/agent", sha, "gate")
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
		p, err := assertedProof(provenSHA)(t).FetchedFrom(id, "acme/repo", ref, sha)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
}

func selected(option string) runspec.Options {
	set := runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "sdk-typescript"}}
	var o runspec.Options
	switch option {
	case "start_at":
		o.StartAt = set
	case "stop_at":
		o.StopAt = set
	case "only":
		o.Only = set
	case "dry_run":
		o.DryRun = runspec.Setting[bool]{Optional: runspec.Optional[bool]{Set: true, Value: true}}
	case "debug":
		o.Debug = runspec.Setting[bool]{Optional: runspec.Optional[bool]{Set: true, Value: true}}
	case "pause_before", "pause_after", "pause_on_failure":
		var pause runspec.PauseSelection
		jobs := runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"gate"}}}
		switch option {
		case "pause_before":
			pause.Before = jobs
		case "pause_after":
			pause.After = jobs
		default:
			pause.OnFailure = runspec.Setting[bool]{Optional: runspec.Optional[bool]{Set: true, Value: true}}
		}
		o.Pause = runspec.Setting[runspec.PauseSelection]{Optional: runspec.Optional[runspec.PauseSelection]{Set: true, Value: pause}}
	}
	return o
}

func provenTenant(t *testing.T, st *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	if err := st.AsOperator().TestOnlyCreateTeam(t.Context(), team); err != nil {
		t.Fatal(err)
	}
	tenant, err := st.ForTeam(t.Context(), team)
	if err != nil {
		t.Fatal(err)
	}
	return tenant
}

func seedProven(t *testing.T, tenant *store.Tenant, id, pipeline string, seed provenSeed, finished time.Time) {
	t.Helper()
	if err := tenant.TestOnlySeedSealedRun(t.Context(), id, pipeline, seed.repo, seed.sha, seed.proof(t), seed.options, finished, seed.env...); err != nil {
		t.Fatal(err)
	}
	if err := tenant.TestOnlySeedNode(t.Context(), id, provenJob, "success", true); err != nil {
		t.Fatal(err)
	}
}

func TestProvenRunAtCommitAcceptsOnlyCleanProvenRunsOfTheReadersRepositoryAndCommit(t *testing.T) {
	reader := provenSeed{repo: provenRepo, sha: provenSHA, proof: appProof(1, provenSHA, true)}
	clean := func(proof func(t *testing.T) runspec.Provenance) provenSeed {
		return provenSeed{repo: provenRepo, sha: provenSHA, proof: proof}
	}
	cases := map[string]struct {
		reader    provenSeed
		candidate provenSeed
		want      bool
	}{
		"clean App-proven branch run":          {candidate: clean(appProof(1, provenSHA, false)), want: true},
		"clean controller-fetched run":         {candidate: clean(fetchedProof(1, provenSHA)), want: true},
		"asserted local run":                   {candidate: clean(assertedProof(provenSHA))},
		"fetched with no proven commit":        {candidate: clean(fetchedProof(1, ""))},
		"proven for another repository":        {candidate: provenSeed{repo: provenOther, sha: provenSHA, proof: appProof(2, provenSHA, false)}},
		"proven at another commit":             {candidate: provenSeed{repo: provenRepo, sha: otherSHA, proof: appProof(1, otherSHA, false)}},
		"asserted reader borrows no proof":     {reader: provenSeed{repo: provenRepo, sha: provenSHA, proof: assertedProof(provenSHA)}, candidate: clean(appProof(1, provenSHA, false))},
		"reader proven for another repository": {reader: provenSeed{repo: provenOther, sha: provenSHA, proof: appProof(2, provenSHA, true)}, candidate: clean(appProof(1, provenSHA, false))},
	}
	for _, option := range []string{"start_at", "stop_at", "only", "dry_run", "debug", "pause_before", "pause_after", "pause_on_failure"} {
		narrowed := clean(appProof(1, provenSHA, false))
		narrowed.options = selected(option)
		cases["proven run with "+option] = struct {
			reader    provenSeed
			candidate provenSeed
			want      bool
		}{candidate: narrowed}
	}
	withEnv := clean(appProof(1, provenSHA, false))
	withEnv.env = []string{"GOFLAGS"}
	cases["proven run with a submitted environment"] = struct {
		reader    provenSeed
		candidate provenSeed
		want      bool
	}{candidate: withEnv}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := storetest.Open(t)
			tenant := provenTenant(t, st, "a")
			now := time.Unix(1700000000, 0)
			seedProven(t, tenant, "candidate", "gate", tc.candidate, now.Add(-time.Minute))
			if tc.reader.proof == nil {
				tc.reader = reader
			}
			seedProven(t, tenant, "reader", "main-ci", tc.reader, now)
			got, err := tenant.GetLatestProvenRunAtCommit(t.Context(), "gate", provenJob, "reader", []string{"success"}, 0)
			if tc.want && (err != nil || got.ID != "candidate") {
				t.Fatalf("want the candidate, got %+v %v", got, err)
			}
			if !tc.want && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("want no proven run, got %+v %v", got, err)
			}
		})
	}
}

func TestProvenRunAtCommitNeverCrossesTeams(t *testing.T) {
	st := storetest.Open(t)
	own := provenTenant(t, st, "a")
	other := provenTenant(t, st, "b")
	now := time.Unix(1700000000, 0)
	seedProven(t, own, "reader", "main-ci", provenSeed{repo: provenRepo, sha: provenSHA, proof: appProof(1, provenSHA, true)}, now)
	seedProven(t, other, "foreign", "gate", provenSeed{repo: provenRepo, sha: provenSHA, proof: appProof(1, provenSHA, false)}, now)
	if got, err := own.GetLatestProvenRunAtCommit(t.Context(), "gate", provenJob, "reader", []string{"success"}, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another team's run satisfied the reader: %+v %v", got, err)
	}
	if _, err := other.GetLatestProvenRunAtCommit(t.Context(), "gate", provenJob, "reader", nil, 0); err == nil {
		t.Fatal("a reader outside the team was accepted")
	}
}

func TestProvenRunAtCommitPassesOverNewerRunsWhoseJobDidNotSucceed(t *testing.T) {
	st := storetest.Open(t)
	tenant := provenTenant(t, st, "a")
	now := time.Unix(1700000000, 0)
	candidate := provenSeed{repo: provenRepo, sha: provenSHA, proof: appProof(1, provenSHA, false)}
	seedProven(t, tenant, "older", "gate", candidate, now.Add(-3*time.Minute))
	for i, outcome := range []string{"skipped", "failed", ""} {
		id := fmt.Sprintf("newer-%d", i)
		if err := tenant.TestOnlySeedSealedRun(t.Context(), id, "gate", candidate.repo, candidate.sha, candidate.proof(t), candidate.options, now.Add(time.Duration(i-2)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if outcome != "" {
			if err := tenant.TestOnlySeedNode(t.Context(), id, provenJob, outcome, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	seedProven(t, tenant, "reader", "main-ci", provenSeed{repo: provenRepo, sha: provenSHA, proof: appProof(1, provenSHA, true)}, now)
	got, err := tenant.GetLatestProvenRunAtCommit(t.Context(), "gate", provenJob, "reader", []string{"success"}, 0)
	if err != nil || got.ID != "older" {
		t.Fatalf("want the older run whose job succeeded, got %+v %v", got, err)
	}
	if got, err := tenant.GetLatestProvenRunAtCommit(t.Context(), "gate", "other-job", "reader", []string{"success"}, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a run without the named job satisfied the reader: %+v %v", got, err)
	}
}

func TestProvenRunAtCommitNeedsEveryJobThatRanOnATrustedHost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome string
		trusted bool
		want    bool
	}{
		{name: "every job on a trusted host", outcome: "success", trusted: true, want: true},
		{name: "a job on a host that takes asserted work", outcome: "success"},
		{name: "a memoized job whose host is unknown", outcome: "memoized"},
		{name: "a skipped job ran nowhere", outcome: "skipped", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.Open(t)
			tenant := provenTenant(t, st, "a")
			now := time.Unix(1700000000, 0)
			seedProven(t, tenant, "candidate", "gate", provenSeed{repo: provenRepo, sha: provenSHA, proof: appProof(1, provenSHA, false)}, now.Add(-time.Minute))
			if err := tenant.TestOnlySeedNode(t.Context(), "candidate", "race", tc.outcome, tc.trusted); err != nil {
				t.Fatal(err)
			}
			seedProven(t, tenant, "reader", "main-ci", provenSeed{repo: provenRepo, sha: provenSHA, proof: appProof(1, provenSHA, true)}, now)
			got, err := tenant.GetLatestProvenRunAtCommit(t.Context(), "gate", provenJob, "reader", []string{"success"}, 0)
			if tc.want && (err != nil || got.ID != "candidate") {
				t.Fatalf("want the candidate, got %+v %v", got, err)
			}
			if !tc.want && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("want no proven run, got %+v %v", got, err)
			}
		})
	}
}

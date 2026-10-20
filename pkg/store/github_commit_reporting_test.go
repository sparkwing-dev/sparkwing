package store

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestGitHubCommitReportingUsesSealedIdentityProperty(t *testing.T) {
	property := func(raw uint32, name, sha string) bool {
		tenant, input, _ := intakeFixture(t)
		repoID := int64(raw) + 1
		input.Request.Git.Repository.Key = runspec.RepoKey(githubRepositoryKey(repoID))
		// Intake seals only full commit IDs.
		sha = fmt.Sprintf("%x", sha1.Sum([]byte(sha)))
		input.Request.Git.SHA = sha
		first, err := tenant.AdmitRun(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		second, err := tenant.AdmitRun(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		unrelated := input
		unrelated.Request.Git.Repository.Key = runspec.RepoKey(githubRepositoryKey(repoID + 1))
		if _, err := tenant.AdmitRun(t.Context(), unrelated); err != nil {
			t.Fatal(err)
		}
		unrelated = input
		unrelated.Request.Git.SHA = fmt.Sprintf("%x", sha1.Sum([]byte(sha+"different")))
		if _, err := tenant.AdmitRun(t.Context(), unrelated); err != nil {
			t.Fatal(err)
		}
		foreign := &Tenant{s: tenant.s, team: "b"}
		root := authorityRoot("foreign", "b")
		root.Grants = []capability.Grant{capability.RunsWrite}
		authorityPersist(t, tenant.s, root)
		unrelated = input
		unrelated.Authority.ID, unrelated.Request.Team = "foreign", "b"
		if _, err := foreign.AdmitRun(t.Context(), unrelated); err != nil {
			t.Fatal(err)
		}
		if id, err := tenant.RecordGitHubCheckRun(t.Context(), first.RunID, 41); err != nil || id != 41 {
			t.Fatalf("first check: %d %v", id, err)
		}
		if id, err := tenant.RecordGitHubCheckRun(t.Context(), first.RunID, 42); err != nil || id != 41 {
			t.Fatalf("replayed check: %d %v", id, err)
		}
		if _, err := foreign.RecordGitHubCheckRun(t.Context(), first.RunID, 43); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign check: %v", err)
		}
		rows, err := tenant.GitHubCommitRuns(t.Context(), GitHubRepo{ID: repoID, Owner: name, Name: name}, sha)
		if err != nil || len(rows) != 2 {
			t.Logf("report count=%d err=%v", len(rows), err)
			return false
		}
		ids := []string{first.RunID, second.RunID}
		slices.Sort(ids)
		if rows[0].ID != ids[1] || rows[1].ID != ids[0] {
			t.Log(fmt.Sprint(rows))
			return false
		}
		for _, id := range []int64{0, repoID + 2} {
			rows, err := tenant.GitHubCommitRuns(t.Context(), GitHubRepo{ID: id, Owner: name, Name: name}, sha)
			if err != nil || len(rows) != 0 {
				t.Fatalf("unmatched repository: %+v %v", rows, err)
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

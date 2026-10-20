package controller

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// 10#5: anyone who can push a tag can publish a release on it, so a release runs
// only when its tag matches the subscription's tag patterns.
func TestReleaseSubscriptionRequiresATagPattern(t *testing.T) {
	env := map[string]string{"GITHUB_TAG_NAME": "v1.2.3"}
	open := store.GitHubAppTrigger{ReleasePublished: true}
	if githubAppSubscribes(open, "release", "published", env, "") {
		t.Fatal("a release with no tag pattern started a run")
	}
	open.Tags = []string{"v*"}
	if !githubAppSubscribes(open, "release", "published", env, "") {
		t.Fatal("a release on a matching tag did not start")
	}
	env["GITHUB_TAG_NAME"] = "attacker"
	if githubAppSubscribes(open, "release", "published", env, "") {
		t.Fatal("a release on an unmatched tag started a run")
	}
}

func pullRequestClosedBody(merged bool, merge string) []byte {
	return fmt.Appendf(nil, `{"number":4,"pull_request":{"updated_at":"2026-01-01T00:00:00Z","merged":%t,"merge_commit_sha":%q,
"head":{"ref":"feature","sha":"1111111111111111111111111111111111111111","repo":{"id":42}},
"base":{"ref":"main","sha":"2222222222222222222222222222222222222222","repo":{"id":42}},"user":{"login":"writer"}}}`, merged, merge)
}

// 10#5: a closed pull request runs only once merged, and then runs the merge commit
// that branch protection admitted, never the head the author still controls.
func TestClosedPullRequestRunsOnlyTheMergeCommit(t *testing.T) {
	env := githubAppDelivery{Action: "closed"}
	env.Repository.ID = 42
	if intake, reason, err := githubAppIntakeFor("pull_request", env, pullRequestClosedBody(false, "")); err != nil || reason == "" || intake.sha != "" {
		t.Fatalf("unmerged closed pull request: %+v %q %v", intake, reason, err)
	}
	merge := "3333333333333333333333333333333333333333"
	intake, reason, err := githubAppIntakeFor("pull_request", env, pullRequestClosedBody(true, merge))
	if err != nil || reason != "" || intake.sha != merge || intake.headRepo != 42 {
		t.Fatalf("merged closed pull request: %+v %q %v", intake, reason, err)
	}
	env.Action = "opened"
	if intake, _, _ := githubAppIntakeFor("pull_request", env, pullRequestClosedBody(false, "")); intake.sha != "1111111111111111111111111111111111111111" {
		t.Fatalf("open pull request runs %q, want its head", intake.sha)
	}
}

// 10#1 and 13 C1: a child names its parent's exact revision or a branch the
// controller resolves; a body that pairs another ref with a commit is refused.
func TestChildCannotPairAnotherRefWithItsOwnCommit(t *testing.T) {
	e, _, _ := childIntakeFixture(t)
	server := New(e.Store, nil)
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}
	request := parent.Request.Request
	request.Pipeline, request.Lineage = "deploy", runspec.Lineage{ParentRunID: "run", ParentJobID: "job"}
	same := request
	if err := server.resolveChildRevisions(t.Context(), e, a, &same); err != nil {
		t.Fatalf("child at its parent's revision refused: %v", err)
	}
	for name, mutate := range map[string]func(*runspec.Request){
		"ref":    func(r *runspec.Request) { r.Git.Ref = "refs/heads/main-elsewhere" },
		"commit": func(r *runspec.Request) { r.Git.SHA = "9999999999999999999999999999999999999999" },
		"code":   func(r *runspec.Request) { r.Source.Ref = "9999999999999999999999999999999999999999" },
	} {
		attack := request
		mutate(&attack)
		var refusal *apierr.Error
		if err := server.resolveChildRevisions(t.Context(), e, a, &attack); !errors.As(err, &refusal) || refusal.Code != apierr.Forbidden {
			t.Fatalf("child with another %s was not refused: %v", name, err)
		}
	}
}

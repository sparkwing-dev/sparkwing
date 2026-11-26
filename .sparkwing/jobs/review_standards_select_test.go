package jobs

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReviewerCardNamesLoadsReviewCardsByAlwaysScopeAndSurface(t *testing.T) {
	listing := `{"name":"runbook-code-review","mode":"runbook"}
{"name":"standard-fixes","apply_type":["code","review"],"apply_always":true}
{"name":"standard-architecture","apply_type":["api","code","review"]}
{"name":"standard-gates","apply_type":["pipeline","review"]}
{"name":"standard-product-rules","apply_type":["review"]}
{"name":"standard-process","apply_type":["pipeline"],"apply_always":true}
{"name":"standard-other-repo","apply_type":["review"],"scope_repo":["elsewhere"]}
{"name":"standard-scoped","apply_type":["review"],"scope_repo":["SPARKWING"]}
`
	rules := `{"kind":"summary"}
{"kind":"rule","card":"standard-fixes","globs":["**/*.go"]}
{"kind":"rule","card":"standard-architecture","globs":["**/*.go","**/api/**"]}
{"kind":"rule","card":"standard-gates","globs":["**/.sparkwing/**","**/.golangci*"]}
{"kind":"rule","card":"standard-product-rules","globs":[]}
{"kind":"rule","card":"standard-process","globs":["**/.sparkwing/**"]}
`
	for _, tc := range []struct {
		files []string
		want  []string
	}{
		{[]string{"internal/controller/server.go"}, []string{"standard-fixes", "standard-architecture", "standard-product-rules", "standard-scoped"}},
		{[]string{".sparkwing/sparkwing.yaml"}, []string{"standard-fixes", "standard-gates", "standard-product-rules", "standard-scoped"}},
		{[]string{".golangci.yml"}, []string{"standard-fixes", "standard-gates", "standard-product-rules", "standard-scoped"}},
		{[]string{"README.txt"}, []string{"standard-fixes", "standard-product-rules", "standard-scoped"}},
	} {
		if got := reviewerCardNames(listing, rules, "sparkwing-dev", "sparkwing", tc.files); !slices.Equal(got, tc.want) {
			t.Errorf("reviewerCardNames(%q) = %q, want %q", tc.files, got, tc.want)
		}
	}
}

func TestReviewStandardsSeeACopyDestinationNotItsUnchangedSource(t *testing.T) {
	fx := newReviewFixture(t)
	root := t.TempDir()
	gitInit(t, root)
	body := "#!/bin/sh\nallowed() { return 0; }\n"
	writeGoFile(t, filepath.Join(root, ".sparkwing/guide.sh"), body)
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD")))
	writeGoFile(t, filepath.Join(root, "guide.sh"), body)
	gitCommitAll(t, root, "copy the guide")
	fx.deps.root = root
	fx.deps.git = func(ctx context.Context, args ...string) (string, error) { return reviewGit(ctx, root, args...) }
	var seen []string
	fx.deps.standards = func(_ context.Context, files []string) (string, error) {
		seen = files
		return "", nil
	}
	fx.def.replies = []string{`{"findings":[]}`}
	if _, _, err := runReview(newTestContext(t), fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Copy a guide"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []string{"guide.sh"}) {
		t.Fatalf("standards saw %q, want only the copy destination", seen)
	}
}

func TestRepoOwnerAndNameReadsOriginNotTheWorktreeName(t *testing.T) {
	for _, tc := range []struct{ origin, owner, repo string }{
		{"git@github.com:sparkwing-dev/sparkwing", "sparkwing-dev", "sparkwing"},
		{"https://github.com/acme/infra.git", "acme", "infra"},
		{"ssh://git@github.com:22/sparkwing-dev/sparkwing.git", "sparkwing-dev", "sparkwing"},
		{"/srv/acme/sparkwing.git", "", "sparkwing"},
		{"", "", "t3-worktree-7"},
	} {
		owner, repo := repoOwnerAndName(tc.origin, "/tmp/t3-worktree-7")
		if owner != tc.owner || repo != tc.repo {
			t.Errorf("repoOwnerAndName(%q) = %q, %q; want %q, %q", tc.origin, owner, repo, tc.owner, tc.repo)
		}
	}
}

package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestVerifiedDefaultBranchNeedsTheVerifiedPushItRuns(t *testing.T) {
	const sha, other = "0123456789abcdef0123456789abcdef01234567", "89abcdef0123456789abcdef0123456789abcdef"
	push := func(ref, gitRef, gitSHA string, defaultPush bool) runspec.Git {
		return runspec.Git{Ref: gitRef, SHA: gitSHA, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Ref: ref, SHA: sha, DefaultBranch: "main", DefaultBranchPush: defaultPush}}}
	}
	for name, tc := range map[string]struct {
		git  runspec.Git
		want bool
	}{
		"default branch push":                    {push("refs/heads/main", "refs/heads/main", sha, true), true},
		"no verified delivery":                   {runspec.Git{Ref: "refs/heads/main", SHA: sha}, false},
		"feature branch push":                    {push("refs/heads/feature", "refs/heads/feature", sha, false), false},
		"main's facts carried to another ref":    {push("refs/heads/main", "refs/heads/feature", sha, true), false},
		"main's facts carried to another commit": {push("refs/heads/main", "refs/heads/main", other, true), false},
		"pull request event on main":             {push("refs/heads/main", "refs/heads/main", sha, false), false},
	} {
		if got := verifiedDefaultBranch(tc.git); got != tc.want {
			t.Errorf("%s: verified default branch = %t, want %t", name, got, tc.want)
		}
	}
}

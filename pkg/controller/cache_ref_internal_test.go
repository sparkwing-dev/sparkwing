package controller

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A claim token's cache scope is its run's repository ID and ref: it writes
// under its own ref and reads its own ref,
// then its pull request's base branch, then the default branch; any other
// grant writes and reads only the unscoped objects, and a grant naming a run
// of another team gets no refs.
func TestCacheRefs_FollowTheRunsRefAsGitHubActionsDoes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for id, trig := range map[string]store.Trigger{
		"run-main": {GitBranch: "main", TriggerEnv: map[string]string{"GITHUB_REF": "refs/heads/main", EnvDefaultBranch: "main"}},
		"run-feat": {GitBranch: "feat", TriggerEnv: map[string]string{"GITHUB_REF": "refs/heads/feat", EnvDefaultBranch: "main"}},
		"run-pr": {GitBranch: "feat", TriggerEnv: map[string]string{
			"GITHUB_REF": "refs/pull/7/head", "GITHUB_BASE_REF": "release", EnvDefaultBranch: "main",
		}},
		"run-manual": {GitBranch: "hotfix"},
		"run-none":   {},
	} {
		trig.ID, trig.Pipeline, trig.CreatedAt = id, "build", time.Now()
		if id != "run-manual" && id != "run-none" {
			trig.GithubRepoID = 41
		}
		if err := st.CreateTrigger(ctx, trig); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(st, nil)
	grant := func(run, kind string) authwire.CacheGrant {
		return authwire.CacheGrant{Team: string(store.DefaultTeam), Run: run, Claim: &authwire.CacheClaim{Kind: kind}}
	}
	for _, c := range []struct {
		run, kind, repo, write string
		read                   []string
	}{
		{"run-main", authwire.CacheClaimToken, "github:41", "refs/heads/main", []string{"refs/heads/main"}},
		{"run-feat", authwire.CacheClaimToken, "github:41", "refs/heads/feat", []string{"refs/heads/feat", "refs/heads/main"}},
		{"run-pr", authwire.CacheClaimToken, "github:41", "refs/pull/7/head", []string{"refs/pull/7/head", "refs/heads/release", "refs/heads/main"}},
		{"run-manual", authwire.CacheClaimToken, "", "refs/heads/hotfix", []string{"refs/heads/hotfix"}},
		{"run-none", authwire.CacheClaimToken, "", "", []string{}},
		{"run-feat", "node", "", "", []string{""}},
	} {
		repo, write, read, err := srv.cacheScope(ctx, grant(c.run, c.kind))
		if err != nil || repo != c.repo || write != c.write || !slices.Equal(read, c.read) {
			t.Errorf("%s %s grant scope = %q %q %q %v, want %q %q %q", c.kind, c.run, repo, write, read, err, c.repo, c.write, c.read)
		}
	}
	other := grant("run-main", authwire.CacheClaimToken)
	other.Team = "other-team"
	if _, _, _, err := srv.cacheScope(ctx, other); err == nil {
		t.Error("a grant naming another team's run got refs")
	}
}

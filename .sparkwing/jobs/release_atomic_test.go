package jobs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func atomicReleaseRepo(t *testing.T, body string) string {
	t.Helper()
	repo := migrationRepo(t, body, twoBreakingGuide, migrationIndexFixture)
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-m", "seed")
	return repo
}

func TestReleaseGateFailureAfterPreparationLeavesHEADUnchanged(t *testing.T) {
	for _, gate := range []string{"schema", "wire"} {
		t.Run(gate, func(t *testing.T) {
			repo := atomicReleaseRepo(t, fixtureChangelog)
			before := gitRun(t, repo, "rev-parse", "HEAD")
			ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{
				"version": json.RawMessage(`"v0.2.0"`),
			}}).Context(t.Context())
			version := sparkwing.Ref[string]{JobID: "version"}
			prepare := prepareChangelogJob{RepoDir: repo, Version: version}
			if err := prepare.run(ctx); err != nil {
				t.Fatalf("prepare changelog: %v", err)
			}
			// safety: an absent origin fails either gate without contacting a remote.
			var err error
			if gate == "schema" {
				err = (&checkSchemaBreakJob{RepoDir: repo, Version: version}).run(ctx)
			} else {
				err = (&checkWireBreakJob{RepoDir: repo, Version: version}).run(ctx)
			}
			if err == nil || !strings.Contains(err.Error(), "resolve previous tag") {
				t.Fatalf("gate failure = %v, want previous-tag refusal", err)
			}
			if got := gitRun(t, repo, "rev-parse", "HEAD"); got != before {
				t.Fatalf("failed %s gate left a release commit: HEAD = %s, want %s", gate, got, before)
			}
			if diff := gitRun(t, repo, "diff", "--cached"); !strings.Contains(diff, "## [v0.2.0]") {
				t.Fatalf("prepared release notes are not staged: %s", diff)
			}
		})
	}
}

func TestReleaseCommitRecordsPreparedFilesOnce(t *testing.T) {
	for key, value := range map[string]string{
		"GIT_CONFIG_COUNT": "4",
		"GIT_CONFIG_KEY_0": "user.name", "GIT_CONFIG_VALUE_0": "Test",
		"GIT_CONFIG_KEY_1": "user.email", "GIT_CONFIG_VALUE_1": "test@example.invalid",
		"GIT_CONFIG_KEY_2": "commit.gpgsign", "GIT_CONFIG_VALUE_2": "false",
		"GIT_CONFIG_KEY_3": "core.hooksPath", "GIT_CONFIG_VALUE_3": "/dev/null",
	} {
		t.Setenv(key, value)
	}
	for _, rolled := range []bool{false, true} {
		t.Run(releaseCommitSubject("v0.9.0", rolled), func(t *testing.T) {
			body := fixtureChangelog
			if rolled {
				body = twoBreakingChangelog
			}
			repo := atomicReleaseRepo(t, body)
			before := gitRun(t, repo, "rev-parse", "HEAD")
			ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{
				"version": json.RawMessage(`"v0.9.0"`),
			}}).Context(t.Context())
			version := sparkwing.Ref[string]{JobID: "version"}
			if err := (&prepareChangelogJob{RepoDir: repo, Version: version}).run(ctx); err != nil {
				t.Fatal(err)
			}
			if got := gitRun(t, repo, "rev-parse", "HEAD"); got != before {
				t.Fatal("preparation created a commit before the gates")
			}
			commit := commitChangelogJob{RepoDir: repo, Version: version}
			if err := commit.run(ctx); err != nil {
				t.Fatal(err)
			}
			after := gitRun(t, repo, "rev-parse", "HEAD")
			if after == before || gitRun(t, repo, "rev-parse", "HEAD^") != before {
				t.Fatal("commit did not advance HEAD exactly once")
			}
			if got := strings.TrimSpace(gitRun(t, repo, "log", "-1", "--format=%s")); got != releaseCommitSubject("v0.9.0", rolled) {
				t.Fatalf("release commit subject = %q", got)
			}
			if err := commit.run(ctx); err != nil {
				t.Fatal(err)
			}
			if gitRun(t, repo, "rev-parse", "HEAD") != after || gitRun(t, repo, "status", "--porcelain") != "" {
				t.Fatal("repeat commit changed HEAD or left uncommitted files")
			}
		})
	}
}

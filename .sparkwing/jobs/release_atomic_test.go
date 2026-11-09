package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func atomicReleaseRepo(t *testing.T, body string) string {
	t.Helper()
	repo := migrationRepo(t, body, twoBreakingGuide, migrationIndexFixture)
	for _, dir := range []string{"pkg/scaffold", ".apidiff", ".sparkwing", kubernetesE2EPipelineModuleRel, "bin"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(repo, scaffoldFallbackRel), "package scaffold\nconst FallbackSDKVersion = \"v0.1.0\"\n")
	writeFile(t, filepath.Join(repo, scaffoldAPISnapshotRel), "const FallbackSDKVersion = \"v0.1.0\"\n")
	writeFile(t, filepath.Join(repo, "go.mod"), "module "+sdkModulePath+"\n\ngo 1.26.0\n")
	writeFile(t, filepath.Join(repo, "sdk.go"), "package sdk\n")
	for _, module := range []string{".sparkwing", kubernetesE2EPipelineModuleRel} {
		writeFile(t, filepath.Join(repo, module, "go.mod"), "module fixture\n\ngo 1.26.0\nrequire "+sdkModulePath+" v0.1.0\nreplace "+sdkModulePath+" => "+filepath.ToSlash(repo)+"\n")
		writeFile(t, filepath.Join(repo, module, "go.sum"), "")
		writeFile(t, filepath.Join(repo, module, "main.go"), "package main\nimport _ \""+sdkModulePath+"\"\nfunc main() {}\n")
	}
	writeFile(t, filepath.Join(repo, "bin/regen-api-snapshot.sh"), "mkdir -p \"$1\"\ncp pkg/scaffold/version.go \"$1/pkg_scaffold.txt\"\n")
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "user.name", "Test")
	gitRun(t, repo, "config", "user.email", "test@example.invalid")
	gitRun(t, repo, "config", "commit.gpgsign", "false")
	gitRun(t, repo, "config", "core.hooksPath", "/dev/null")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "seed")
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
			_, plan, _ := preparePlan(t, func(_ context.Context, plan *sparkwing.Plan) error {
				sparkwing.Job(plan, "version", &resolveVersionJob{Explicit: "v0.9.0", RepoDir: repo})
				sparkwing.Job(plan, "commit", &commit)
				return nil
			})
			host := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{
				"version": json.RawMessage(`"v0.9.0"`),
			}})
			output, err := host.RunStep(t.Context(), plan, "commit", "run")
			if err != nil {
				t.Fatal(err)
			}
			after := gitRun(t, repo, "rev-parse", "HEAD")
			if string(output) != `"`+strings.TrimSpace(after)+`"` {
				t.Fatalf("commit output = %s, want release commit %s", output, after)
			}
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

func TestReleaseCommitPreservesUnrelatedStagedFiles(t *testing.T) {
	repo := atomicReleaseRepo(t, fixtureChangelog)
	ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"version": json.RawMessage(`"v0.9.0"`)}}).Context(t.Context())
	version := sparkwing.Ref[string]{JobID: "version"}
	if err := (&prepareChangelogJob{RepoDir: repo, Version: version}).run(ctx); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "unrelated.txt"), "keep staged")
	gitRun(t, repo, "add", "--", "unrelated.txt")
	hooks := t.TempDir()
	writeDeliveryTool(t, filepath.Join(hooks, "pre-commit"), "#!/bin/sh\nexit 1\n")
	gitRun(t, repo, "config", "core.hooksPath", hooks)
	if err := (&commitChangelogJob{RepoDir: repo, Version: version}).run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitRun(t, repo, "diff", "--cached", "--name-only")); got != "unrelated.txt" {
		t.Fatalf("staged files = %q", got)
	}
	if got, aligned, err := coherentReleaseVersionArtifacts(repo); err != nil || !aligned || got != "v0.9.0" {
		t.Fatalf("pins = %s, %v, %v", got, aligned, err)
	}
	for _, rel := range []string{scaffoldFallbackRel, scaffoldAPISnapshotRel, ".sparkwing/go.mod", kubernetesE2EPipelineModuleRel + "/go.mod"} {
		if body := gitRun(t, repo, "show", "HEAD:"+rel); !strings.Contains(body, "v0.9.0") {
			t.Fatalf("committed pin %s: %s", rel, body)
		}
	}
	gitRun(t, repo, "tag", "v0.9.0")
	if problem := checkScaffoldFallbackPin(ctx, repo); problem != "" {
		t.Fatalf("next cut sees stale pins: %s", problem)
	}
}

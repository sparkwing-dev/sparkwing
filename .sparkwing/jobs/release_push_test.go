package jobs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func releaseGitIdentity(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"GIT_CONFIG_COUNT": "4",
		"GIT_CONFIG_KEY_0": "user.name", "GIT_CONFIG_VALUE_0": "Test",
		"GIT_CONFIG_KEY_1": "user.email", "GIT_CONFIG_VALUE_1": "test@example.invalid",
		"GIT_CONFIG_KEY_2": "commit.gpgsign", "GIT_CONFIG_VALUE_2": "false",
		"GIT_CONFIG_KEY_3": "core.hooksPath", "GIT_CONFIG_VALUE_3": "/dev/null",
	} {
		t.Setenv(key, value)
	}
}

func TestReleasePushKeepsTheChosenSourceWhileMainMoves(t *testing.T) {
	for _, mode := range []string{"main already advanced", "main moves during push", "fragment notes", "push refused", "missing commit", "missing source"} {
		t.Run(mode, func(t *testing.T) {
			releaseGitIdentity(t)
			sourceBody := fixtureChangelog
			fragments := mode == "fragment notes"
			if fragments {
				start, end, _, err := changelogSectionBounds(sourceBody, "Unreleased")
				if err != nil {
					t.Fatal(err)
				}
				sourceBody = sourceBody[:start] + "## [Unreleased]\n\n" + sourceBody[end:]
			}
			repo := atomicReleaseRepo(t, sourceBody)
			if fragments {
				for _, dir := range releaseFragmentDirs[:2] {
					writeReleaseFixture(t, filepath.Join(repo, dir, "chosen.md"), "### Fixed\n\n- **release:** Chosen source notes.\n")
				}
				gitRun(t, repo, "add", "--", "docs/changes", "pkg/docs/mirror/changes")
			}
			writeFile(t, filepath.Join(repo, ".gitattributes"), "CHANGELOG.md merge=union\npkg/docs/changelog.md merge=union\n")
			gitRun(t, repo, "add", "--", ".gitattributes")
			gitRun(t, repo, "commit", "--no-verify", "-m", "merge policy")
			source := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
			origin := t.TempDir()
			gitRun(t, origin, "init", "--bare", "-b", "main")
			gitRun(t, repo, "remote", "add", "origin", origin)
			gitRun(t, repo, "push", "origin", "main")
			detached := filepath.Join(t.TempDir(), "release")
			gitRun(t, repo, "worktree", "add", "--detach", detached, source)
			collected, err := readChangelog(detached)
			if err != nil {
				t.Fatal(err)
			}
			body, err := rewriteUnreleasedToVersion(string(collected), "v0.99.0", "2026-01-01")
			if err != nil {
				t.Fatal(err)
			}
			if err := writeChangelogPair(detached, body); err != nil {
				t.Fatal(err)
			}
			gitRun(t, detached, "add", "--", "CHANGELOG.md", embeddedChangelogRel)
			if fragments {
				consumed, err := consumeReleaseFragments(detached)
				if err != nil {
					t.Fatal(err)
				}
				gitRun(t, detached, append([]string{"add", "--"}, consumed...)...)
			}
			gitRun(t, detached, "commit", "--no-verify", "-m", "release notes")
			commit := strings.TrimSpace(gitRun(t, detached, "rev-parse", "HEAD"))

			newer := strings.Replace(fixtureChangelog, "## [Unreleased]", "## [Unreleased]\n\n### Added\n\n- **later:** Keep this for the next release.", 1)
			if fragments {
				newer = sourceBody
				for _, dir := range releaseFragmentDirs[:2] {
					writeReleaseFixture(t, filepath.Join(repo, dir, "later.md"), "### Added\n\n- **later:** Keep this for the next release.\n")
				}
				gitRun(t, repo, "add", "--", "docs/changes", "pkg/docs/mirror/changes")
			}
			if err := writeChangelogPair(repo, newer); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(repo, "later.go"), "package sdk\nconst Later = true\n")
			gitRun(t, repo, "add", "--", "CHANGELOG.md", embeddedChangelogRel, "later.go")
			gitRun(t, repo, "commit", "--no-verify", "-m", "a later land")
			advanced := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
			if mode != "main moves during push" {
				gitRun(t, repo, "push", "origin", "main")
			} else {
				gitRun(t, repo, "push", "origin", "HEAD:refs/heads/race-candidate")
			}
			before := strings.TrimSpace(gitRun(t, origin, "rev-parse", "main"))

			if mode == "main moves during push" || mode == "push refused" {
				realGit, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				t.Setenv("RELEASE_TEST_REAL_GIT", realGit)
				t.Setenv("RELEASE_TEST_ORIGIN", origin)
				t.Setenv("RELEASE_TEST_ADVANCED", advanced)
				t.Setenv("RELEASE_TEST_ONCE", filepath.Join(bin, "once"))
				t.Setenv("RELEASE_TEST_MODE", mode)
				writeDeliveryTool(t, filepath.Join(bin, "git"), `#!/bin/sh
set -eu
if [ "$1" = push ] && [ "$RELEASE_TEST_MODE" = "push refused" ]; then exit 1; fi
if [ "$1" = push ] && [ "$2" = --atomic ] && [ ! -f "$RELEASE_TEST_ONCE" ]; then
 touch "$RELEASE_TEST_ONCE"
 "$RELEASE_TEST_REAL_GIT" --git-dir="$RELEASE_TEST_ORIGIN" update-ref refs/heads/main "$RELEASE_TEST_ADVANCED"
fi
exec "$RELEASE_TEST_REAL_GIT" "$@"
`)
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			output := commit
			if mode == "missing commit" {
				output = ""
			}
			selected := source
			if mode == "missing source" {
				selected = ""
			}
			ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{
				"version": json.RawMessage(`"v0.99.0"`), "commit": json.RawMessage(`"` + output + `"`),
			}}).Context(t.Context())
			writeFile(t, filepath.Join(repo, "local-draft"), "keep my work")
			caller := gitRun(t, repo, "status", "--porcelain")
			job := pushTagJob{RepoDir: detached, Source: selected, Version: sparkwing.Ref[string]{JobID: "version"}, Commit: sparkwing.Ref[string]{JobID: "commit"}}
			err = job.run(ctx)
			refused := mode == "push refused" || mode == "missing commit" || mode == "missing source"
			if (err != nil) != refused {
				t.Fatalf("publication = %v, want refusal %v", err, refused)
			}
			if gitRun(t, repo, "status", "--porcelain") != caller || strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD")) != advanced {
				t.Fatal("publication changed caller checkout")
			}
			if refused {
				if strings.TrimSpace(gitRun(t, origin, "rev-parse", "main")) != before || strings.TrimSpace(gitRun(t, origin, "tag", "--list")) != "" {
					t.Fatal("failed atomic publication changed main or tags")
				}
				return
			}
			if got := strings.TrimSpace(gitRun(t, origin, "rev-parse", "v0.99.0^{commit}")); got != commit {
				t.Fatalf("tag = %s, want detached release %s", got, commit)
			}
			gitRun(t, origin, "merge-base", "--is-ancestor", commit, "main")
			gitRun(t, origin, "merge-base", "--is-ancestor", advanced, "main")
			if got := gitRun(t, origin, "show", "main:later.go"); !strings.Contains(got, "Later") {
				t.Fatal("lost later source")
			}
			notes := gitRun(t, origin, "show", "main:CHANGELOG.md")
			_, _, pending, err := changelogSectionBounds(notes, "Unreleased")
			if err != nil {
				t.Fatal(err)
			}
			if fragments {
				if len(pending.entries) != 0 || !strings.Contains(gitRun(t, origin, "show", "main:docs/changes/later.md"), "later") ||
					strings.TrimSpace(gitRun(t, origin, "ls-tree", "main", "--", "docs/changes/chosen.md")) != "" || !strings.Contains(notes, "Chosen source notes.") {
					t.Fatal("publication lost later fragments or retained consumed fragments")
				}
			} else if len(pending.entries) != 1 || !strings.Contains(pending.entries[0].body, "later") {
				t.Fatalf("pending notes = %v", pending.entries)
			}
			if mirror := gitRun(t, origin, "show", "main:"+embeddedChangelogRel); mirror != notes {
				t.Fatal("changelog mirror drifted")
			}
			if got := gitRun(t, origin, "show", "v0.99.0:CHANGELOG.md"); strings.Contains(got, "later") {
				t.Fatal("newer note leaked into tag")
			}
		})
	}
}

func TestReleasePushRunsOnTheDispatcherHost(t *testing.T) {
	if !mustNode(t, releasePlan(t), "push-tag").Inline {
		t.Fatal("push-tag must run in the release worktree")
	}
}

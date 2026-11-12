package jobs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseCLIWithConcurrentMainActor(t *testing.T) {
	cli := os.Getenv("SPARKWING_LOCAL_E2E_BINARY")
	if cli == "" {
		t.Skip("set SPARKWING_LOCAL_E2E_BINARY to a CLI built from this worktree")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	pin, err := readPipelineModulePin(root, ".sparkwing")
	if err != nil {
		t.Fatal(err)
	}
	version, err := bumpVersion(pin, "minor")
	if err != nil {
		t.Fatal(err)
	}
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprintf("refused_%t", refuse), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
			defer cancel()
			repo := atomicReleaseRepo(t, fixtureChangelog)
			write := func(rel, body string) {
				t.Helper()
				path := filepath.Join(repo, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				writeFile(t, path, body)
			}
			write(".gitattributes", "CHANGELOG.md merge=union\npkg/docs/changelog.md merge=union\n")
			write("internal/store/store.go", "package store\nconst expectedSchemaVersion = 1\n")
			write("internal/store/schema.sql", "CREATE TABLE fixture(id TEXT);\n")
			for _, rel := range []string{scaffoldFallbackRel, scaffoldAPISnapshotRel, kubernetesE2EPipelineModuleRel + "/go.mod"} {
				path := filepath.Join(repo, rel)
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, path, strings.ReplaceAll(string(body), "v0.1.0", pin))
			}
			write(".sparkwing/main.go", "package main\nimport (_ \"sparkwing-pipelines/jobs\"; sw \"github.com/sparkwing-dev/sparkwing/sparkwing\")\nfunc main(){sw.Main()}\n")
			write(".sparkwing/sparkwing.yaml", "pipelines:\n  - {name: release, entrypoint: Release}\n  - {name: release-cut, entrypoint: ReleaseCut}\n")
			write(".sparkwing/go.mod", fmt.Sprintf("module release-fixture\ngo 1.26.0\nrequire (\n github.com/sparkwing-dev/sparkwing v0.1.0\n sparkwing-pipelines v0.0.0\n)\nreplace github.com/sparkwing-dev/sparkwing => %s\nreplace sparkwing-pipelines => %s/.sparkwing\n", root, root))
			// safety: policy subprocesses are faked so the fixture judges release orchestration and Git publication.
			tools := t.TempDir()
			realGo, err := exec.LookPath("go")
			if err != nil {
				t.Fatal(err)
			}
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			writeDeliveryTool(t, filepath.Join(tools, "go"), fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n 'run ./internal/doccheck --sidebar docs'|'test ./cmd/sparkwing -run ^TestPipelineScaffoldDoesNotRequireGoPatch$ -count=1'|'run ./cmd/verify-release --index-out '*|'mod download %s@v0.1.0') exit 0;;\nesac\nexec %q \"$@\"\n", sdkModulePath, realGo))
			for _, tool := range []string{"curl", "node"} {
				writeDeliveryTool(t, filepath.Join(tools, tool), "#!/bin/sh\nexit 0\n")
			}
			for _, module := range []string{".sparkwing", kubernetesE2EPipelineModuleRel} {
				cmd := exec.CommandContext(ctx, realGo, "-C", filepath.Join(repo, module), "mod", "tidy")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fixture tidy: %v: %s", err, out)
				}
			}
			gitRun(t, repo, "add", ".")
			gitRun(t, repo, "-c", "commit.gpgsign=false", "commit", "--no-verify", "-m", "release fixture")
			source := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
			gitRun(t, repo, "-c", "tag.gpgsign=false", "tag", pin)
			digest, err := sdkSourceDigest(newTestContext(t), repo, source)
			if err != nil {
				t.Fatal(err)
			}
			gitRun(t, repo, "-c", "tag.gpgsign=false", "tag", "-a", "sparkwing/v0.1.0", source,
				"-m", "SDK v0.1.0\n\n"+sdkDigestMarker+digest)
			origin := t.TempDir()
			gitRun(t, origin, "init", "--bare", "-b", "main")
			gitRun(t, origin, "config", "core.hooksPath", filepath.Join(origin, "hooks"))
			gitRun(t, repo, "remote", "add", "origin", origin)
			gitRun(t, repo, "push", "origin", "main", "--tags")
			actor := filepath.Join(t.TempDir(), "actor")
			gitRun(t, repo, "clone", origin, actor)
			writeFile(t, filepath.Join(actor, "later.txt"), "second actor source must survive\n")
			newer := strings.Replace(fixtureChangelog, "## [Unreleased]", "## [Unreleased]\n\n### Added\n\n- **later:** Keep the second actor note pending.", 1)
			for _, rel := range []string{"CHANGELOG.md", embeddedChangelogRel} {
				writeFile(t, filepath.Join(actor, rel), newer)
			}
			gitRun(t, actor, "add", ".")
			gitRun(t, actor, "-c", "commit.gpgsign=false", "commit", "--no-verify", "-m", "concurrent main change")
			advanced := strings.TrimSpace(gitRun(t, actor, "rev-parse", "HEAD"))
			if refuse {
				writeDeliveryTool(t, filepath.Join(origin, "hooks/pre-receive"), "#!/bin/sh\nwhile read -r old new ref; do\n case \"$ref\" in refs/tags/"+version+") echo fixture-tag-refused >&2; exit 1;; esac\ndone\n")
			}
			barrier := t.TempDir()
			writeDeliveryTool(t, filepath.Join(tools, "git"), fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$1" = push ] && [ "${2-}" = --atomic ] && [ ! -e %q/once ]; then
 touch %q/once %q/publishing
 while [ ! -e %q/advanced ]; do sleep 0.02; done
fi
exec %q -c user.name=Acceptance -c user.email=acceptance@example.invalid -c commit.gpgsign=false -c tag.gpgsign=false "$@"
`, barrier, barrier, barrier, barrier, realGit))
			writeDeliveryTool(t, filepath.Join(tools, "gh"), fmt.Sprintf("#!/bin/sh\nprintf '[{\"headSha\":\"%s\",\"status\":\"completed\",\"conclusion\":\"success\"}]\\n'\n", source))
			writeDeliveryTool(t, filepath.Join(tools, "bitwing"), "#!/bin/sh\nif [ \"$2\" = start ]; then echo '{\"id\":\"fixture-workflow\"}'; fi\n")
			if err := os.Symlink(cli, filepath.Join(tools, "sparkwing")); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			config := filepath.Join(home, "config.yaml")
			writeFile(t, config, "admission: {budget: ignore-external}\n")
			if err := os.Chmod(config, 0o600); err != nil {
				t.Fatal(err)
			}
			environment := append(os.Environ(), "PATH="+tools+":"+os.Getenv("PATH"), "HOME="+home, "SPARKWING_HOME="+filepath.Join(home, "state"), "SPARKWING_CONFIG="+config, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "SPARKWING_SECRETS_KEY_FILE="+filepath.Join(home, "key"), "GOWORK=off", "GOFLAGS=-buildvcs=false -p=1", "GOMAXPROCS=2")
			cmd := exec.CommandContext(ctx, cli, "run", "release", "-C", repo, "--sw-no-update", "--sw-allow-risk", "destructive,prod", "-o", "json", "--", "--version", version, "--commit", source)
			cmd.Env = environment
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				stopDaemon := exec.CommandContext(cleanup, cli, "daemon", "stop", "-o", "json")
				stopDaemon.Env = environment
				out, err := stopDaemon.CombinedOutput()
				if err != nil {
					t.Errorf("private controller cleanup: %v: %s", err, out)
				}
			})
			for {
				if _, err := os.Stat(filepath.Join(barrier, "publishing")); err == nil {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("release exited before publication: %v: %s", err, output.String())
				case <-ctx.Done():
					t.Fatalf("publication barrier: %v: %s", ctx.Err(), output.String())
				// sleepcheck:external-boundary wait for the real Git publication subprocess
				case <-time.After(20 * time.Millisecond):
				}
			}
			gitRun(t, actor, "push", "origin", "main")
			writeFile(t, filepath.Join(barrier, "advanced"), "")
			err = <-done
			if (err != nil) != refuse {
				t.Fatalf("release refusal=%t: %v: %s", refuse, err, output.String())
			}
			if got := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD")); got != source {
				t.Fatal("release moved the caller checkout")
			}
			if refuse {
				if got := strings.TrimSpace(gitRun(t, origin, "rev-parse", "main")); got != advanced || strings.Contains(gitRun(t, origin, "tag", "--list"), version) {
					t.Fatal("refused atomic publication changed remote main or published tag")
				}
				t.Logf("source=%s actor=%s: rejected tag left main at the actor commit", source, advanced)
				return
			}
			cut := strings.TrimSpace(gitRun(t, origin, "rev-parse", version+"^{commit}"))
			if strings.TrimSpace(gitRun(t, origin, "rev-parse", cut+"^")) != source {
				t.Fatal("release tag changed its pinned source")
			}
			gitRun(t, origin, "merge-base", "--is-ancestor", cut, "main")
			gitRun(t, origin, "merge-base", "--is-ancestor", advanced, "main")
			if got := gitRun(t, origin, "show", "main:later.txt"); !strings.Contains(got, "second actor") {
				t.Fatal("concurrent main source was lost")
			}
			notes := gitRun(t, origin, "show", "main:CHANGELOG.md")
			_, _, pending, err := changelogSectionBounds(notes, "Unreleased")
			if err != nil || len(pending.Entries) != 1 || !strings.Contains(pending.Entries[0].Body, "second actor") {
				t.Fatalf("pending notes: %+v: %v", pending, err)
			}
			if strings.Contains(gitRun(t, origin, "show", cut+":CHANGELOG.md"), "second actor") || notes != gitRun(t, origin, "show", "main:"+embeddedChangelogRel) {
				t.Fatal("release source or merged changelog mirror drifted")
			}
			t.Logf("selected=%s release=%s actor=%s: immutable tag and metadata merge survived concurrent push", source, cut, advanced)
		})
	}
}

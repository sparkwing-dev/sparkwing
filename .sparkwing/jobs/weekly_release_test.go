package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestWeeklyReleaseUsesFreshMainAndTheExistingCut(t *testing.T) {
	for _, mode := range []string{"release", "dry-run", "fragments", "empty", "red", "pending", "missing", "chosen", "unlanded", "manual-red", "manual-pending", "manual-missing", "manual-unavailable", "manual-malformed", "manual-wrong-sha"} {
		t.Run(mode, func(t *testing.T) {
			body := fixtureChangelog
			if mode == "empty" || mode == "fragments" {
				body = "# Changelog\n\n## [Unreleased]\n\n## [v0.1.0] - 2026-01-01\n\n- Old release.\n"
			}
			repo := atomicReleaseRepo(t, body)
			if mode == "fragments" {
				writeReleaseFixture(t, filepath.Join(repo, "docs/changes/weekly.md"), "### Fixed\n\n- **weekly:** Release collected notes\n")
				gitRun(t, repo, "add", "docs/changes/weekly.md")
				gitRun(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--no-verify", "-m", "pending fragment")
			}
			origin := t.TempDir()
			gitRun(t, origin, "init", "--bare", "-b", "main")
			gitRun(t, repo, "remote", "add", "origin", origin)
			gitRun(t, repo, "push", "origin", "main")
			head := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
			if mode == "chosen" {
				writeFile(t, filepath.Join(repo, "later.txt"), "after the chosen commit")
				gitRun(t, repo, "add", "--", "later.txt")
				gitRun(t, repo, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-m", "later main")
				gitRun(t, repo, "push", "origin", "main")
			}
			gitRun(t, repo, "checkout", "-b", "unlanded")
			if mode == "unlanded" {
				writeFile(t, filepath.Join(repo, "unlanded.txt"), "never pushed")
				gitRun(t, repo, "add", "--", "unlanded.txt")
				gitRun(t, repo, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-m", "unlanded work")
				head = strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
			}
			writeFile(t, filepath.Join(repo, "CHANGELOG.md"), "uncommitted local work must not be released")
			before := gitRun(t, repo, "status", "--porcelain")
			bin := t.TempDir()
			call := filepath.Join(t.TempDir(), "cut.json")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("RELEASE_TEST_CALL", call)
			status, conclusion := "completed", "success"
			if mode == "red" || mode == "manual-red" {
				conclusion = "failure"
			}
			if mode == "pending" || mode == "manual-pending" {
				status, conclusion = "in_progress", ""
			}
			ci, _ := json.Marshal([]mainCIRun{{HeadSHA: head, Status: status, Conclusion: conclusion}})
			if mode == "missing" || mode == "manual-missing" {
				ci = []byte("[]")
			}
			if mode == "manual-malformed" {
				ci = []byte("not JSON")
			}
			if mode == "manual-wrong-sha" {
				ci = []byte(`[{"headSha":"other","status":"completed","conclusion":"success"}]`)
			}
			t.Setenv("RELEASE_TEST_CI", string(ci))
			writeDeliveryTool(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' \"$RELEASE_TEST_CI\"\n")
			if mode == "manual-unavailable" {
				writeDeliveryTool(t, filepath.Join(bin, "gh"), "#!/bin/sh\nexit 1\n")
			}
			writeDeliveryTool(t, filepath.Join(bin, "bitwing"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$RELEASE_TEST_CALL.workflow"
if [ "$2" = start ]; then printf '{"id":"WR-123"}\n'; fi
`)
			writeDeliveryTool(t, filepath.Join(bin, "sparkwing"), `#!/bin/sh
set -eu
[ "$SPARKWING_SECRETS_KEY_FILE" = "$SPARKWING_HOME/secrets.key" ]
if [ "$1" = daemon ]; then
  printf '%s\n' "$*" > "$RELEASE_TEST_CALL.stop"
  exit
fi
printf '%s\n' "$@" > "$RELEASE_TEST_CALL"
printf '%s\n' "$SPARKWING_HOME" >> "$RELEASE_TEST_CALL"
git -C "$4" rev-parse HEAD >> "$RELEASE_TEST_CALL"
if git -C "$4" symbolic-ref -q HEAD; then exit 90; fi
case "$*" in *--sw-dry-run*) ;; *) git -C "$4" tag v0.1.0 ;; esac
`)
			ctx := newTestHost(t, sparkwingtest.Options{}).Context(t.Context())
			args := ReleaseArgs{Bump: "patch"}
			weekly := mode != "chosen" && mode != "unlanded" && !strings.HasPrefix(mode, "manual-")
			if !weekly {
				args.Commit = head
			}
			err := (&weeklyReleaseJob{RepoDir: repo, Args: args, Weekly: weekly}).run(ctx, mode == "dry-run")
			refused := mode == "red" || mode == "pending" || mode == "missing" || mode == "unlanded"
			if (err != nil) != refused {
				t.Fatalf("release = %v, want refusal %v", err, refused)
			}
			got, readErr := os.ReadFile(call)
			if refused || mode == "empty" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("release cut ran for %s: %s", mode, got)
				}
			} else {
				if readErr != nil {
					t.Fatal(readErr)
				}
				text := string(got)
				for _, want := range []string{"run\nrelease-cut\n-C\n", "--version\nv0.1.0\n", "--commit\n" + head} {
					if !strings.Contains(text, want) {
						t.Fatalf("cut call lacks %q: %s", want, text)
					}
				}
				if got, want := strings.Contains(text, "--sw-start-at\nversion-freshness\n"), !strings.HasPrefix(mode, "manual-"); got != want {
					t.Fatalf("CI reuse = %v, want %v: %s", got, want, text)
				}
				if strings.Contains(text, "--sw-dry-run") != (mode == "dry-run") {
					t.Fatalf("dry-run propagation: %s", text)
				}
				if strings.Contains(text, "--sw-allow-risk") != (mode != "dry-run") {
					t.Fatalf("risk propagation: %s", text)
				}
				workflow, workflowErr := os.ReadFile(call + ".workflow")
				if mode == "dry-run" {
					if !os.IsNotExist(workflowErr) {
						t.Fatalf("preview wrote workflow: %s", workflow)
					}
				} else {
					for _, want := range []string{"start --name sparkwing-release", "--idempotency-key sparkwing-release-v0.1.0", "--step cut --status blocked", "immutable tag v0.1.0 at " + head, "sparkwing-product", "kikd-infra", "downstream pins"} {
						if !strings.Contains(string(workflow), want) {
							t.Fatalf("workflow handoff lacks %q: %s (%v)", want, workflow, workflowErr)
						}
					}
				}
				stop, err := os.ReadFile(call + ".stop")
				if err != nil || strings.TrimSpace(string(stop)) != "daemon stop" {
					t.Fatalf("private controller cleanup = %q, %v", stop, err)
				}
			}
			if after := gitRun(t, repo, "status", "--porcelain"); after != before {
				t.Fatalf("release changed caller's checkout: %s", after)
			}
			if tags := gitRun(t, origin, "tag", "--list"); strings.TrimSpace(tags) != "" {
				t.Fatalf("preview or refused run pushed tags: %s", tags)
			}
		})
	}
}

func writeDeliveryTool(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestWeeklyReleaseRejectsWrongSHAAndMalformedCI(t *testing.T) {
	for _, body := range []string{`[{"headSha":"other","status":"completed","conclusion":"success"}]`, "not JSON"} {
		if err := checkMainCI(body, "selected"); err == nil {
			t.Fatalf("accepted CI receipt %s", body)
		}
	}
}

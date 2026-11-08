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
	for _, mode := range []string{"release", "dry-run", "empty", "red", "pending", "missing"} {
		t.Run(mode, func(t *testing.T) {
			body := fixtureChangelog
			if mode == "empty" {
				body = "# Changelog\n\n## [Unreleased]\n\n## [v0.1.0] - 2026-01-01\n\n- Old release.\n"
			}
			repo := atomicReleaseRepo(t, body)
			origin := t.TempDir()
			gitRun(t, origin, "init", "--bare", "-b", "main")
			gitRun(t, repo, "remote", "add", "origin", origin)
			gitRun(t, repo, "push", "origin", "main")
			head := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
			gitRun(t, repo, "checkout", "-b", "unlanded")
			writeFile(t, filepath.Join(repo, "CHANGELOG.md"), "uncommitted local work must not be released")
			before := gitRun(t, repo, "status", "--porcelain")
			bin := t.TempDir()
			call := filepath.Join(t.TempDir(), "cut.json")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("RELEASE_TEST_CALL", call)
			status, conclusion := "completed", "success"
			if mode == "red" {
				conclusion = "failure"
			}
			if mode == "pending" {
				status, conclusion = "in_progress", ""
			}
			ci, _ := json.Marshal([]mainCIRun{{HeadSHA: head, Status: status, Conclusion: conclusion}})
			if mode == "missing" {
				ci = []byte("[]")
			}
			t.Setenv("RELEASE_TEST_CI", string(ci))
			writeDeliveryTool(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' \"$RELEASE_TEST_CI\"\n")
			writeDeliveryTool(t, filepath.Join(bin, "sparkwing"), `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$RELEASE_TEST_CALL"
printf '%s\n' "$SPARKWING_HOME" >> "$RELEASE_TEST_CALL"
git -C "$4" rev-parse HEAD >> "$RELEASE_TEST_CALL"
`)
			ctx := newTestHost(t, sparkwingtest.Options{}).Context(t.Context())
			err := (&weeklyReleaseJob{RepoDir: repo, Args: ReleaseArgs{Bump: "patch"}}).run(ctx, mode == "dry-run")
			refused := mode == "red" || mode == "pending" || mode == "missing"
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
				for _, want := range []string{"run\nrelease\n-C\n", "--bump\npatch\n", head} {
					if !strings.Contains(text, want) {
						t.Fatalf("cut call lacks %q: %s", want, text)
					}
				}
				if strings.Contains(text, "--sw-dry-run") != (mode == "dry-run") {
					t.Fatalf("dry-run propagation: %s", text)
				}
				if strings.Contains(text, "--sw-allow-risk") != (mode == "release") {
					t.Fatalf("risk propagation: %s", text)
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

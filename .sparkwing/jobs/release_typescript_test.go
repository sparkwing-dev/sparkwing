//go:build !windows

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func TestReleaseTypeScriptDryRun(t *testing.T) {
	for _, tc := range []struct {
		name, version, want string
		tamper              bool
	}{
		{name: "changed source under published version", version: "0.72.1", want: "SDK version already names different source"},
		{name: "changed source with newer version", version: "0.72.2"},
		{name: "unauthenticated policy", version: "0.72.2", tamper: true, want: "version index signature is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) { runTypeScriptReleasePreview(t, tc.version, tc.tamper, tc.want) })
	}
}

func runTypeScriptReleasePreview(t *testing.T, version string, tamper bool, want string) {
	t.Helper()
	repo := cloneCheckout(t)
	editSDKSource(t, repo)
	setSDKVersion(t, repo, version)
	mode := ""
	if tamper {
		mode = "tamper"
	}
	stubPublicReleaseIndex(t, mode)
	t.Setenv("GITHUB_EVENT_NAME", "")
	t.Setenv("GITHUB_OUTPUT", "")
	t.Chdir(repo)
	beforeHead := gitRun(t, repo, "rev-parse", "HEAD")
	beforeTags := gitRun(t, repo, "tag", "--list")
	name := fmt.Sprintf("typescript-release-preview-%d", testPipelineNumber.Add(1))
	var plan *sparkwing.Plan
	sparkwing.Register(name, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return testPipeline{build: func(_ context.Context, built *sparkwing.Plan) error {
			plan = built
			sparkwing.Job(built, "checks", testJob(func(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
				return sparkwing.Step(w, "typescript-release", func(ctx context.Context) error {
					return checkTypeScriptRelease(ctx, repo)
				}).SafeWithoutDryRun(), nil
			}))
			return nil
		}}
	})
	host := newTestHost(t, sparkwingtest.Options{Run: sparkwing.RunContext{DryRun: true}})
	if _, err := host.Plan(t.Context(), name, nil); err != nil {
		t.Fatal(err)
	}
	_, err := host.RunStep(t.Context(), plan, "checks", "typescript-release")
	if want == "" {
		if err != nil {
			t.Fatalf("dry-run refused new SDK version: %v", err)
		}
	} else if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("dry-run = %v, want %q", err, want)
	}
	if got := gitRun(t, repo, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatal("refused preview committed release metadata")
	}
	if got := gitRun(t, repo, "tag", "--list"); got != beforeTags {
		t.Fatal("refused preview created a release tag")
	}
}

func stubPublicReleaseIndex(t *testing.T, mode string) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(root, ".sparkwing/jobs/testdata/typescript-release-policy")
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	tamper := ":"
	if mode == "tamper" {
		tamper = `printf ' ' >> "$output"`
	}
	unreachable := ":"
	if mode == "unreachable" {
		unreachable = `echo 'curl: (6) Could not resolve host: sparkwing.dev' >&2; exit 6`
	}
	writeDeliveryTool(t, filepath.Join(bin, "curl"), fmt.Sprintf(`#!/bin/sh
set -eu
url= output=
while [ "$#" -gt 0 ]; do
 case "$1" in
  https://sparkwing.dev/*) url=$1; shift ;;
  -o) output=$2; shift 2 ;;
  *) shift ;;
 esac
done
echo "$url" >> %s
%s
case "$url" in
 https://sparkwing.dev/versions.json) cp %s "$output"; %s ;;
 https://sparkwing.dev/versions.json.sig) cp %s "$output" ;;
 *) exit 41 ;;
esac
`, shellQuote(calls), unreachable, shellQuote(filepath.Join(policy, "versions.json")), tamper, shellQuote(filepath.Join(policy, "versions.json.sig"))))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func TestSDKVersionPolicyRefusesChangedSourceUnderPublishedVersion(t *testing.T) {
	for _, tc := range []struct {
		name, version, index, want, warn string
		change                           func(t *testing.T, repo string)
		fetched                          bool
	}{
		// safety: the signed fixture publishes v0.72.1 under a different digest.
		{
			name: "native build toolchain bump under published version", version: "0.72.1", change: bumpNativeGoVersion, fetched: true,
			want: "bump the version in sdk/typescript/package.json, sdk/typescript/package-lock.json and typeScriptSDKVersion in cmd/sparkwing/action_new_typescript.go",
		},
		{
			name: "SDK source edit under published version", version: "0.72.1", change: editSDKSource, fetched: true,
			want: "SDK version already names different source",
		},
		{
			name: "digest code change under published version", version: "0.72.1", change: editDigestCode, fetched: true,
			want: "SDK version already names different source",
		},
		{name: "change outside the SDK release inputs", version: "0.72.1", change: editReadme},
		{name: "toolchain bump with a new unpublished version", version: "0.72.2", change: bumpNativeGoVersion, fetched: true},
		{
			name: "index unreachable", version: "0.72.1", index: "unreachable", change: bumpNativeGoVersion, fetched: true,
			warn: "sdk-version: UNVERIFIED: the published SDK index is unavailable",
		},
		{
			name: "index fails verification", version: "0.72.1", index: "tamper", change: bumpNativeGoVersion, fetched: true,
			warn: "sdk-version: UNVERIFIED: the published SDK index is unavailable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := sdkVersionFixture(t, tc.version)
			tc.change(t, repo)
			gitCommitAll(t, repo, "candidate")
			calls := stubPublicReleaseIndex(t, tc.index)
			t.Setenv("GITHUB_EVENT_NAME", "")
			t.Setenv("GITHUB_OUTPUT", "")
			t.Chdir(repo)
			host := newTestHost(t, sparkwingtest.Options{})
			err := policyStepRun(t, "sdk-version")(host.Context(t.Context()))
			if tc.want == "" && err != nil {
				t.Fatalf("sdk-version refused a releasable change: %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("sdk-version = %v, want %q", err, tc.want)
			}
			if _, statErr := os.Stat(calls); (statErr == nil) != tc.fetched {
				t.Fatalf("index fetched = %v, want %v", statErr == nil, tc.fetched)
			}
			var log strings.Builder
			for _, record := range host.Records() {
				log.WriteString(record.Level + " " + record.Msg + "\n")
			}
			if tc.warn != "" && !strings.Contains(log.String(), "warn "+tc.warn) {
				t.Fatalf("an unverifiable index passed without naming it:\n%s", log.String())
			}
		})
	}
}

func policyStepRun(t *testing.T, id string) func(context.Context) error {
	t.Helper()
	for _, step := range policySteps {
		if step.id == id {
			return step.run
		}
	}
	t.Fatalf("no policy step %q", id)
	return nil
}

// safety: the base commit is origin/main, so the change in scope is only the candidate's.
func sdkVersionFixture(t *testing.T, version string) string {
	t.Helper()
	repo := cloneCheckout(t)
	setSDKVersion(t, repo, version)
	gitCommitAll(t, repo, "base")
	gitRun(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	return repo
}

func bumpNativeGoVersion(t *testing.T, repo string) {
	t.Helper()
	path := filepath.Join(repo, ".github/workflows/release.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(body), "\n  typescript-native:")
	end := strings.Index(string(body), "\n  typescript-package:")
	if start < 0 || end < start {
		t.Fatal("release workflow has no typescript-native job")
	}
	job := regexp.MustCompile(`go-version: "[^"]*"`).ReplaceAllString(string(body[start:end]), `go-version: "9.99.9"`)
	if job == string(body[start:end]) {
		t.Fatal("typescript-native job names no go-version")
	}
	writeFile(t, path, string(body[:start])+job+string(body[end:]))
}

func editSDKSource(t *testing.T, repo string) {
	t.Helper()
	path := filepath.Join(repo, "sdk/typescript/src/main.ts")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body)+"\nexport const changedReleaseFixture = true;\n")
}

func editReadme(t *testing.T, repo string) {
	t.Helper()
	path := filepath.Join(repo, "README.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body)+"\nFixture edit.\n")
}

func setSDKVersion(t *testing.T, repo, version string) {
	t.Helper()
	manifest := filepath.Join(repo, "sdk/typescript/package.json")
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(body, &pkg); err != nil {
		t.Fatal(err)
	}
	pkg["version"] = version
	body, err = json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, manifest, string(body))
}

// safety: hosted CI soft-resets HEAD to the event base and keeps the candidate
// in the index, so the clone checks out the index tree rather than HEAD.
func cloneCheckout(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	// safety: a commit starts detached auto maintenance on newer git, and its
	// commit-graph write into a clone with long history can outlive a fast test.
	gitRun(t, root, "clone", "--quiet", "--shared", "-c", "maintenance.auto=false", "-c", "gc.auto=0", root, repo)
	tree := strings.TrimSpace(gitOutput(t, root, "write-tree"))
	if tree != strings.TrimSpace(gitOutput(t, repo, "rev-parse", "HEAD^{tree}")) {
		gitRun(t, repo, "read-tree", "--reset", "-u", tree)
		gitCommitAll(t, repo, "checkout files")
	}
	if got := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "HEAD^{tree}")); got != tree {
		t.Fatalf("fixture tree %s does not match the checkout index tree %s", got, tree)
	}
	return repo
}

func TestCloneCheckoutStartsNoAutoMaintenance(t *testing.T) {
	repo := cloneCheckout(t)
	for key, want := range map[string]string{"maintenance.auto": "false", "gc.auto": "0"} {
		if got := strings.TrimSpace(gitOutput(t, repo, "config", "--local", "--get", key)); got != want {
			t.Errorf("clone config %s = %q, want %q", key, got, want)
		}
	}
}

func editDigestCode(t *testing.T, repo string) {
	t.Helper()
	path := filepath.Join(repo, "bin/typescript-release.mjs")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(body), `"native-build-inputs\0"`, `"native-build-inputs-v2\0"`, 1)
	if changed == string(body) {
		t.Fatal("bin/typescript-release.mjs names no native-build-inputs domain")
	}
	writeFile(t, path, changed)
}

func TestMergeRecheckJudgesTheTypeScriptSDKVersion(t *testing.T) {
	stubPublicReleaseIndex(t, "")
	root := publishedSDKFixture(t)
	writeAPISnapshotFixture(t, root, "")
	for _, check := range []string{"changelog", "api-spec"} {
		writeGoFile(t, filepath.Join(root, "bin", "check-"+check+".sh"), "exit 0\n")
	}
	writeGoFile(t, filepath.Join(root, "cmd/verify-release/main.go"), "package main\n\nfunc main() {}\n")
	writeGoFile(t, filepath.Join(root, "bin/typescript-release.mjs"), "console.error(\"merge contract sdk-version failed\");\nprocess.exit(1);\n")
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "sdk/typescript/src/main.ts"), "export const merged = true;\n")
	gitCommitAll(t, root, "SDK change")
	if err := os.MkdirAll(filepath.Join(root, "web", "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := newTestHost(t, sparkwingtest.Options{})
	err := recheckMergeDelta(host.Context(t.Context()), []string{"internal/sound.go"})
	if err == nil || !strings.Contains(err.Error(), "merge contract sdk-version failed") {
		var log strings.Builder
		for _, record := range host.Records() {
			log.WriteString(record.Msg + "\n")
		}
		t.Fatalf("merged tree skipped the TypeScript SDK version: %v\n%s", err, log.String())
	}
}

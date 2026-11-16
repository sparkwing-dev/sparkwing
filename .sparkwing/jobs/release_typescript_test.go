//go:build !windows

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	gitRun(t, root, "clone", "--shared", root, repo)
	path := filepath.Join(repo, "sdk/typescript/src/main.ts")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body)+"\nexport const changedReleaseFixture = true;\n")
	manifest := filepath.Join(repo, "sdk/typescript/package.json")
	body, err = os.ReadFile(manifest)
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
	policy := filepath.Join(root, ".sparkwing/jobs/testdata/typescript-release-policy")
	bin := t.TempDir()
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
case "$url" in
 https://sparkwing.dev/versions.json) cp %s "$output"; %s ;;
 https://sparkwing.dev/versions.json.sig) cp %s "$output" ;;
 *) exit 41 ;;
esac
`, shellQuote(filepath.Join(policy, "versions.json")), func() string {
		if tamper {
			return `printf ' ' >> "$output"`
		}
		return ":"
	}(), shellQuote(filepath.Join(policy, "versions.json.sig"))))
	t.Setenv("GITHUB_EVENT_NAME", "")
	t.Setenv("GITHUB_OUTPUT", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	_, err = host.RunStep(t.Context(), plan, "checks", "typescript-release")
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

//go:build !windows

package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

const publishedDocsFixtureLatest = "v0.72.4"

type publishedDocsSite struct {
	llms        string
	llmsMissing bool
	tamperIndex bool
	originTags  []string
	localTags   []string
}

func TestPublishedDocsAcceptTheLatestPublishedRelease(t *testing.T) {
	repo := publishedDocsRepo(t)
	for name, site := range map[string]publishedDocsSite{
		"documented":                    {originTags: []string{"v0.72.4"}},
		"newer tag without publication": {originTags: []string{"v0.72.4", "v0.90.0"}, localTags: []string{"v0.91.0"}},
		"newer prerelease tag":          {originTags: []string{"v0.72.4", "v0.90.0-rc.1"}},
	} {
		t.Run(name, func(t *testing.T) {
			site.llms = "# sparkwing -- v0.72.4 (latest)\n\n> docs\n"
			if err := runPublishedDocsCheck(t, repo, site); err != nil {
				t.Fatalf("documented latest published release refused: %v", err)
			}
		})
	}
}

func TestPublishedDocsRefuseDocsOlderThanTheLatestPublishedRelease(t *testing.T) {
	err := runPublishedDocsCheck(t, publishedDocsRepo(t), publishedDocsSite{
		llms:       "# sparkwing -- v0.71.0 (latest)\n",
		originTags: []string{"v0.72.4"},
	})
	if err == nil || !strings.Contains(err.Error(), "documents v0.71.0 but v0.72.4 is the latest published release") {
		t.Fatalf("stale public docs refusal = %v", err)
	}
}

func TestPublishedDocsRefuseAnIndexTitleOutsideTheGeneratedFormat(t *testing.T) {
	repo := publishedDocsRepo(t)
	for name, llms := range map[string]string{
		"title not on the first line": "# llms\n# sparkwing -- v0.72.4 (latest)\n",
		"trailing text":               "# sparkwing -- v0.72.4 (latest) draft\n",
		"leading zero":                "# sparkwing -- v0.072.4 (latest)\n",
		"prerelease":                  "# sparkwing -- v0.72.4-rc.1 (latest)\n",
		"empty":                       "",
	} {
		t.Run(name, func(t *testing.T) {
			err := runPublishedDocsCheck(t, repo, publishedDocsSite{llms: llms, originTags: []string{"v0.72.4"}})
			if err == nil || !strings.Contains(err.Error(), "must begin with") {
				t.Fatalf("malformed docs index %q = %v", llms, err)
			}
		})
	}
}

func TestPublishedDocsFailClosedWhenTheSiteCannotBeTrusted(t *testing.T) {
	repo := publishedDocsRepo(t)
	for name, tc := range map[string]struct {
		site publishedDocsSite
		want string
	}{
		"docs index unreachable": {publishedDocsSite{llmsMissing: true}, "download https://sparkwing.dev/llms.txt"},
		"index signature broken": {publishedDocsSite{llms: "# sparkwing -- v0.72.4 (latest)\n", tamperIndex: true}, "version index signature is invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.site.originTags = []string{"v0.72.4"}
			if err := runPublishedDocsCheck(t, repo, tc.site); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("untrusted site = %v, want %q", err, tc.want)
			}
		})
	}
}

func publishedDocsRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	gitRun(t, root, "clone", "--quiet", "--shared", root, repo)
	return repo
}

func runPublishedDocsCheck(t *testing.T, repo string, site publishedDocsSite) error {
	t.Helper()
	origin := t.TempDir()
	gitRun(t, origin, "init", "--quiet", "-b", "main")
	gitRun(t, origin, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false",
		"commit", "--quiet", "--allow-empty", "-m", "origin fixture")
	for _, tag := range site.originTags {
		gitRun(t, origin, "tag", tag)
	}
	gitRun(t, repo, "remote", "set-url", "origin", origin)
	for _, tag := range site.localTags {
		gitRun(t, repo, "tag", "-f", tag)
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(root, ".sparkwing/jobs/testdata/typescript-release-policy")
	bin := t.TempDir()
	llms := filepath.Join(bin, "llms.txt")
	writeFile(t, llms, site.llms)
	llmsStep := fmt.Sprintf("cp %s \"$output\"", shellQuote(llms))
	if site.llmsMissing {
		llmsStep = "echo 'curl: (22) The requested URL returned error: 404' >&2; exit 22"
	}
	tamper := ":"
	if site.tamperIndex {
		tamper = `printf ' ' >> "$output"`
	}
	// safety: a stalled site must fail the check rather than hold the cut, so every
	// fetch needs a deadline and the docs title fetch reads only its head.
	writeDeliveryTool(t, filepath.Join(bin, "curl"), fmt.Sprintf(`#!/bin/sh
set -eu
url= output= deadline= head=
while [ "$#" -gt 0 ]; do
 case "$1" in
  https://*) url=$1; shift ;;
  -o) output=$2; shift 2 ;;
  --max-time) deadline=$2; shift 2 ;;
  --range) head=$2; shift 2 ;;
  *) shift ;;
 esac
done
[ -n "$deadline" ] || { echo "curl: unbounded fetch of $url" >&2; exit 43; }
case "$url" in
 %[1]sllms.txt) [ "$head" = 0-4095 ] || { echo "curl: unbounded read of $url" >&2; exit 43; }; %[2]s ;;
 %[1]sversions.json) cp %[3]s "$output"; %[4]s ;;
 %[1]sversions.json.sig) cp %[5]s "$output" ;;
 *) exit 41 ;;
esac
`, "https://sparkwing.dev/", llmsStep, shellQuote(filepath.Join(policy, "versions.json")), tamper,
		shellQuote(filepath.Join(policy, "versions.json.sig"))))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(repo)

	name := fmt.Sprintf("published-docs-%d", testPipelineNumber.Add(1))
	var plan *sparkwing.Plan
	sparkwing.Register(name, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return testPipeline{build: func(_ context.Context, built *sparkwing.Plan) error {
			plan = built
			sparkwing.Job(built, "checks", testJob(func(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
				return sparkwing.Step(w, "published-docs", func(ctx context.Context) error {
					return checkPublishedDocs(ctx, repo)
				}).SafeWithoutDryRun(), nil
			}))
			return nil
		}}
	})
	host := newTestHost(t, sparkwingtest.Options{Run: sparkwing.RunContext{DryRun: true}})
	if _, err := host.Plan(t.Context(), name, nil); err != nil {
		t.Fatal(err)
	}
	_, err = host.RunStep(t.Context(), plan, "checks", "published-docs")
	return err
}

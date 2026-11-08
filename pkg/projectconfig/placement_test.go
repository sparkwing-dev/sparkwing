package projectconfig

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const placementRevision = "0123456789abcdef0123456789abcdef01234567"

func TestPlacementResolvesPipelineListAndJobOverrides(t *testing.T) {
	raw := `pipelines:
  - name: pre-release
    entrypoint: PreRelease
    placement:
      prefer:
        - target: team
          hold: 5s
        - target: cloud
    jobs:
      macos:
        placement:
          prefer:
            - target: team
              labels: [os=darwin]
              hold: 5s
            - target: github-actions
              workflow: .github/workflows/sparkwing-fallback.yaml
              runner: macos-latest
      linux:
        placement:
          prefer: []
      docs: {}
`
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := cfg.RepositoryPlacement("pre-release", placementRevision)
	if err != nil || policy == nil || policy.Validate() != nil {
		t.Fatalf("policy: %+v %v", policy, err)
	}
	if got := policy.ForJob("docs"); len(got) != 2 || got[0].Hold != 5*time.Second || got[1].Target != runspec.PlacementCloud || got[1].Hold != 0 {
		t.Fatalf("a job without an override inherits the pipeline list with a 0s default hold: %+v", got)
	}
	if got := policy.ForJob("linux"); got == nil || len(got) != 0 {
		t.Fatalf("an explicit empty list restores existing routing: %#v", got)
	}
	if got := policy.ForJob("macos"); len(got) != 2 || got[0].Labels[0] != "os=darwin" || got[1].Runner != "macos-latest" {
		t.Fatalf("a job override replaces the pipeline list: %+v", got)
	}
	if other, err := cfg.RepositoryPlacement("absent", placementRevision); other != nil || err != nil {
		t.Fatalf("an unknown pipeline has no placement: %+v %v", other, err)
	}
}

func TestPlacementOmittedKeepsExistingRouting(t *testing.T) {
	cfg, err := Parse([]byte("pipelines: [{name: build, entrypoint: Build}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if policy, err := cfg.RepositoryPlacement("build", placementRevision); policy != nil || err != nil {
		t.Fatalf("omitted placement sealed a policy: %+v %v", policy, err)
	}
}

func TestPlacementRefusesInvalidListsWithTheirLocation(t *testing.T) {
	entry := func(body string) string {
		return "pipelines:\n  - name: build\n    entrypoint: Build\n" + body
	}
	list := func(tiers string) string { return entry("    placement:\n      prefer:\n" + tiers) }
	for name, test := range map[string]struct{ raw, key, problem string }{
		"unknown target":      {list("        - target: here\n"), "pipelines[0].placement.prefer[0]", "must be team"},
		"pool is burst-only":  {list("        - target: pool=gpu\n"), "pipelines[0].placement.prefer[0]", "must be team"},
		"empty name":          {list("        - target: name=\n"), "pipelines[0].placement.prefer[0]", "must be team"},
		"labels on cloud":     {list("        - target: cloud\n          labels: [os=linux]\n"), "pipelines[0].placement.prefer[0]", "take no labels"},
		"blank label":         {list("        - target: team\n          labels: [\"os=linux,\"]\n"), "pipelines[0].placement.prefer[0]", "invalid placement label"},
		"bad hold":            {list("        - target: team\n          hold: soon\n"), "pipelines[0].placement.prefer[0].hold", "nonnegative duration"},
		"negative hold":       {list("        - target: team\n          hold: -1s\n"), "pipelines[0].placement.prefer[0].hold", "nonnegative duration"},
		"hold overflow":       {list("        - target: team\n          hold: 2562047h\n        - target: cloud\n          hold: 2562047h\n"), "pipelines[0].placement.prefer[1]", "sum must fit"},
		"actions not last":    {list("        - target: github-actions\n          workflow: .github/workflows/f.yaml\n          runner: ubuntu-latest\n        - target: team\n"), "pipelines[0].placement.prefer[1]", "must be the last"},
		"actions no workflow": {list("        - target: github-actions\n          runner: ubuntu-latest\n"), "pipelines[0].placement.prefer[0]", "name a workflow"},
		"workflow elsewhere":  {list("        - target: github-actions\n          workflow: ci/fallback.yaml\n          runner: ubuntu-latest\n"), "pipelines[0].placement.prefer[0].workflow", ".github/workflows/"},
		"runner on team":      {list("        - target: team\n          runner: ubuntu-latest\n"), "pipelines[0].placement.prefer[0]", "name a workflow"},
		"duplicate tier":      {list("        - target: team\n        - target: team\n"), "pipelines[0].placement.prefer[1]", "listed twice"},
		"local route":         {entry("    run: {route: local}\n    placement:\n      prefer:\n        - target: cloud\n"), "pipelines[0].placement.prefer[0].target", "conflicts with pipelines[0].run.route"},
		"defaults route":      {"defaults: {run: {route: local}}\n" + entry("    jobs:\n      deploy:\n        placement:\n          prefer:\n            - target: team\n            - target: cloud\n"), "pipelines[0].jobs.deploy.placement.prefer[1].target", "conflicts with defaults.run.route"},
		"job override null":   {entry("    jobs:\n      deploy: null\n"), "", ""},
		"unknown job field":   {entry("    jobs:\n      deploy:\n        requires: [os=darwin]\n"), "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(test.raw))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), test.key) || !strings.Contains(err.Error(), test.problem) {
				t.Fatalf("error %q does not name %s: %s", err, test.key, test.problem)
			}
		})
	}
}

func TestPlacementTeamTiersAcceptLabelAlternativesAndNames(t *testing.T) {
	raw := `pipelines:
  - name: build
    entrypoint: Build
    placement:
      prefer:
        - target: name=build-mac
          labels: ["os=darwin,os=macos", arch=arm64]
          hold: 1m
        - target: team
          labels: [os=darwin]
        - target: team
`
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := cfg.RepositoryPlacement("build", placementRevision)
	if err != nil || len(policy.Prefer) != 3 || policy.Prefer[0].Hold != time.Minute {
		t.Fatalf("%+v %v", policy, err)
	}
}

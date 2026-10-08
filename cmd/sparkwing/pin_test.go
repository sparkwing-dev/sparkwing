package main

import (
	"strings"
	"testing"
)

const pinnedProject = `
pipelines:
  - name: release
    entrypoint: Release
    profile: prod
  - name: build
    entrypoint: Build
`

func TestRun_RefusesAPinnedPipelineAndNamesTrigger(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  prod: { controller: { url: http://127.0.0.1:1 } }\n")
	projectAt(t, pinnedProject)
	err := dispatchRun([]string{"release"})
	if err == nil || !strings.Contains(err.Error(), "pipeline release is pinned to profile prod; use sparkwing pipeline trigger --profile prod release") {
		t.Fatalf("dispatchRun(release) = %v, want the pin refusal", err)
	}
}

func TestPipelineTrigger_RefusesAProfileOtherThanThePin(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  prod: { controller: { url: http://127.0.0.1:1 } }\n  staging: { controller: { url: http://127.0.0.1:1 } }\n")
	projectAt(t, pinnedProject)
	err := runPipelineTrigger([]string{"release", "--profile", "staging"})
	if err == nil || !strings.Contains(err.Error(), "pipeline release is pinned to profile prod; pass --profile prod") {
		t.Fatalf("trigger release --profile staging = %v, want the pin refusal", err)
	}
	if err := refuseOtherThanPinnedProfile("release", "prod"); err != nil {
		t.Fatalf("the pinned profile was refused: %v", err)
	}
	if err := refuseOtherThanPinnedProfile("build", "staging"); err != nil {
		t.Fatalf("an unpinned pipeline was refused: %v", err)
	}
}

func TestPipelineTrigger_ThePinIsNoDefault(t *testing.T) {
	projectAt(t, pinnedProject)
	err := runPipelineTrigger([]string{"release"})
	if err == nil || !strings.Contains(err.Error(), "--profile NAME is required") {
		t.Fatalf("trigger release with no --profile = %v, want --profile required", err)
	}
}

func TestPipelineDescribe_ReportsThePin(t *testing.T) {
	projectAt(t, pinnedProject)
	catalog, err := gatherPipelinesCatalog(false)
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string]string{}
	for _, p := range catalog {
		pins[p.Name] = p.PinnedProfile
	}
	if pins["release"] != "prod" || pins["build"] != "" {
		t.Fatalf("pinned profiles = %v, want release pinned to prod and build unpinned", pins)
	}
}

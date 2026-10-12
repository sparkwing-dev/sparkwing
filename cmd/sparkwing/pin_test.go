package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
	if err == nil || !strings.Contains(err.Error(), "pipeline release is pinned to profile prod; use sparkwing pipeline trigger release --profile prod") {
		t.Fatalf("dispatchRun(release) = %v, want the pin refusal", err)
	}
}

func TestPipelineTrigger_RefusesAProfileOtherThanThePin(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  prod: { controller: { url: http://127.0.0.1:1 } }\n  staging: { controller: { url: http://127.0.0.1:1 } }\n")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	projectAt(t, pinnedProject)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q")
	err = runPipelineTrigger([]string{"release", "--profile", "staging", "--working-tree"})
	if err == nil || !strings.Contains(err.Error(), "pipeline release is pinned to profile prod; pass --profile prod") {
		t.Fatalf("trigger release --profile staging = %v, want the pin refusal", err)
	}
	if err := refuseOtherThanPinnedProfile("release", "prod", true); err != nil {
		t.Fatalf("the pinned profile was refused: %v", err)
	}
	if err := refuseOtherThanPinnedProfile("build", "staging", true); err != nil {
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

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// With --sw-ref the pin comes from the revision that executes, not from the
// checkout the command was typed in.
func TestRun_SwRefReadsThePinAtTheExecutedRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	writeProfilesFixture(t, "profiles:\n  prod: { controller: { url: http://127.0.0.1:1 } }\n")
	projectAt(t, pinnedProject)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "pinned")
	gitIn(t, root, "tag", "pinned")
	unpinned := "pipelines:\n  - name: release\n    entrypoint: Release\n"
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"), []byte(unpinned), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "commit", "-q", "-am", "unpinned")
	gitIn(t, root, "tag", "unpinned")

	err = dispatchRun([]string{"release", "--sw-ref", "pinned"})
	if err == nil || !strings.Contains(err.Error(), "pinned to profile prod") {
		t.Fatalf("--sw-ref to a pinned revision from an unpinned checkout = %v, want the pin refusal", err)
	}
	dir := filepath.Join(root, ".sparkwing")
	if err := os.WriteFile(filepath.Join(dir, "sparkwing.yaml"), []byte(pinnedProject), 0o644); err != nil {
		t.Fatal(err)
	}
	if entry, err := pinnedPipelineAt(dir, "unpinned", "release"); err != nil || entry == nil || entry.Profile != "" {
		t.Fatalf("pin at the unpinned revision = %+v, %v; want the unpinned entry though the checkout pins it", entry, err)
	}
}

// The command a refusal suggests must be one pipeline trigger accepts: the
// parser requires the pipeline name before any flag.
func TestPinRefusal_SuggestsACommandTriggerParses(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  prod: { controller: { url: http://127.0.0.1:1 } }\n")
	projectAt(t, pinnedProject)
	for _, args := range [][]string{{"release"}, {"build", "--profile", "prod"}} {
		err := dispatchRun(args)
		if err == nil {
			t.Fatalf("dispatchRun(%q) succeeded", args)
		}
		_, suggested, ok := strings.Cut(err.Error(), "sparkwing pipeline trigger ")
		if !ok {
			t.Fatalf("dispatchRun(%q) = %v, names no trigger command", args, err)
		}
		flags, perr := parseTriggerFlags(strings.Fields(suggested))
		if perr != nil || flags.pipeline != args[0] || flags.profile == "" {
			t.Fatalf("suggested %q parses to %+v, %v; want pipeline %s with a --profile", suggested, flags, perr, args[0])
		}
	}
}

// Without --working-tree the controller runs HEAD, so HEAD's sparkwing.yaml
// holds the pin; with it the uploaded snapshot does.
func TestPipelineTrigger_ReadsThePinFromTheSubmittedRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	projectAt(t, pinnedProject)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "pinned")
	unpinned := "pipelines:\n  - name: release\n    entrypoint: Release\n"
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"), []byte(unpinned), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := refuseOtherThanPinnedProfile("release", "staging", false); err == nil || !strings.Contains(err.Error(), "pinned to profile prod") {
		t.Fatalf("trigger of HEAD, which pins release to prod = %v, want the pin refusal", err)
	}
	if err := refuseOtherThanPinnedProfile("release", "staging", true); err != nil {
		t.Fatalf("--working-tree uploads the unpinned working tree, yet the pin was enforced: %v", err)
	}
}

// The pin lives in sparkwing.yaml, so a .sparkwing/ without main.go still pins
// the pipeline, and a config that does not parse refuses the trigger.
func TestPipelineTrigger_ReadsThePinWithoutMainGoAndFailsOnABadConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	swDir := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(swDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(swDir, "sparkwing.yaml"), []byte(pinnedProject), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "pinned, no main.go")
	for _, workingTree := range []bool{false, true} {
		if err := refuseOtherThanPinnedProfile("release", "staging", workingTree); err == nil || !strings.Contains(err.Error(), "pinned to profile prod") {
			t.Fatalf("workingTree=%v: trigger --profile staging = %v, want the pin refusal", workingTree, err)
		}
	}
	if err := os.WriteFile(filepath.Join(swDir, "sparkwing.yaml"), []byte("pipelines: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "commit", "-q", "-am", "broken")
	for _, workingTree := range []bool{false, true} {
		if err := refuseOtherThanPinnedProfile("release", "staging", workingTree); err == nil {
			t.Fatalf("workingTree=%v: an unparseable sparkwing.yaml read as no pin", workingTree)
		}
	}
}

// The runner compiles the root .sparkwing/, so a config-only .sparkwing/ in a
// subdirectory must not hide the root pipeline's pin.
func TestPipelineTrigger_ANestedConfigDoesNotHideTheRootPin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	projectAt(t, pinnedProject)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "service", ".sparkwing")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "sparkwing.yaml"), []byte("pipelines:\n  - name: release\n    entrypoint: Release\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "root pins release; a nested config does not")
	t.Chdir(filepath.Dir(nested))
	for _, workingTree := range []bool{false, true} {
		if err := refuseOtherThanPinnedProfile("release", "staging", workingTree); err == nil || !strings.Contains(err.Error(), "pinned to profile prod") {
			t.Fatalf("workingTree=%v: trigger from the nested directory = %v, want the root pin refusal", workingTree, err)
		}
	}
}

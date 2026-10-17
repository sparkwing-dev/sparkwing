package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseRunFlagsFinalGrammar(t *testing.T) {
	flags, args := parseRunFlagsArgs([]string{"--sw-only=build-*", "--sw-no-memo", "--sw-allow-risk", "deploy,prod", "--sw-pause-before", "build,test", "--sw-pause-after=publish", "--sw-pause-on-failure=false", "--sw-run-handle-file=/tmp/result", "--", "--sw-no-cache", "--", "value"})
	if flags.parseErr != nil {
		t.Fatal(flags.parseErr)
	}
	if flags.only != "build-*" || !flags.noCache || !slices.Equal(flags.allow, []string{"deploy", "prod"}) || flags.runHandleFile != "/tmp/result" {
		t.Fatalf("flags: %#v", flags)
	}
	if !slices.Equal(flags.pauseBefore, []string{"build", "test"}) || !slices.Equal(flags.pauseAfter, []string{"publish"}) || flags.pauseOnFailure == nil || *flags.pauseOnFailure {
		t.Fatalf("pause flags: %#v", flags)
	}
	if !slices.Equal(args, []string{"--sw-no-cache", "--", "value"}) {
		t.Fatalf("pipeline args: %v", args)
	}
}

func TestParseRunFlagsRejectsInvalidBeforeExecution(t *testing.T) {
	for _, args := range [][]string{
		{"--sw-only"},
		{"--sw-index"},
		{"--sw-workers=0"},
		{"--sw-workers=-1"},
		{"--sw-workers=x"},
		{"--sw-no-cache"},
		{"--sw-allow=prod"},
		{"--profile=prod"},
		{"--sw-profile=prod"},
		{"--unknown"},
		{"--sw-detached", "--sw-workers=1"},
		{"--sw-request-id=id"},
		{"--sw-consumer-idle=1m"},
	} {
		flags, args := parseRunFlagsArgs(args)
		if flags.parseErr == nil || exitCodeFor(flags.parseErr) != 2 || len(args) != 0 {
			t.Fatalf("accepted invalid args: %#v %v", flags, args)
		}
	}
}

func TestParseRunFlagsRetainsPublicationSelection(t *testing.T) {
	flags, _ := parseRunFlagsArgs([]string{"--publish=ci", "--sw-cache-from=ci", "--target=arm64"})
	if flags.parseErr != nil || flags.publish != "ci" || flags.cacheFrom != "ci" || flags.target != "arm64" {
		t.Fatalf("selection lost: %#v", flags)
	}
}

func TestDispatchRunValidationPrecedesSideEffects(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("SPARKWING_HOME", home)
	for _, args := range [][]string{{"build", "--profile=prod"}, {"build", "--sw-workers=0"}, {"build", "--unknown"}} {
		if err := dispatchRunArgs(args); exitCodeFor(err) != 2 {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("created state: %v", err)
	}
}

func TestSetupRefWorktreeCleanupLeavesNothingRegistered(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture", "--allow-empty"},
	} {
		if out, err := runGit(root, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeRepoFile(t, filepath.Join(root, ".sparkwing", "main.go"), "package main\n\nfunc main() {}\n")
	if out, err := runGit(root, "add", "."); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := runGit(root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "-qm", "pipelines"); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}

	worktree, pipelineDir, cleanup, err := setupRefWorktree(filepath.Join(root, ".sparkwing"), "HEAD")
	if err != nil {
		t.Fatalf("setupRefWorktree: %v", err)
	}
	if _, err := os.Stat(pipelineDir); err != nil {
		t.Fatalf("checked-out pipeline directory: %v", err)
	}

	cleanup()
	cleanup()

	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("worktree directory %s survived cleanup: %v", worktree, err)
	}
	list, err := runGit(root, "worktree", "list")
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if strings.Contains(list, worktree) {
		t.Errorf("worktree still registered:\n%s", list)
	}
}

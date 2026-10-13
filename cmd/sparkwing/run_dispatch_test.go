package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseRunFlags_Only(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--sw-only", "fictional-stage-*"}, "fictional-stage-*"},
		{"equals-form", []string{"--sw-only=fictional-stage-*"}, "fictional-stage-*"},
		{"empty-trailing-flag-falls-through", []string{"--sw-only"}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			flags, passthroughArgs := parseRunFlags(testCase.args)
			if flags.only != testCase.want {
				t.Errorf("only = %q, want %q", flags.only, testCase.want)
			}
			if testCase.want == "" && !slices.Contains(passthroughArgs, "--sw-only") {
				t.Errorf("incomplete --sw-only should pass through; got passthrough=%v", passthroughArgs)
			}
		})
	}
}

func TestParseRunFlags_NoCache(t *testing.T) {
	flags, passthroughArgs := parseRunFlags([]string{"--sw-no-cache"})
	if !flags.noCache {
		t.Errorf("noCache: want true got false")
	}
	if len(passthroughArgs) != 0 {
		t.Errorf("passthrough should be empty, got %v", passthroughArgs)
	}
}

func TestParseRunFlags_RunHandleFile(t *testing.T) {
	flags, passthroughArgs := parseRunFlags([]string{"--sw-run-handle-file", "/tmp/fictional-run.json", "--target", "fictional-production"})
	if flags.runHandleFile != "/tmp/fictional-run.json" {
		t.Fatalf("runHandleFile = %q", flags.runHandleFile)
	}
	if got := strings.Join(passthroughArgs, " "); got != "--target fictional-production" {
		t.Fatalf("passthrough = %q", got)
	}
}

func TestRemoveEnvDropsAmbientRunHandle(t *testing.T) {
	env := removeEnv([]string{"PATH=/bin", "SPARKWING_RUN_HANDLE_FILE=/tmp/fictional-stale"}, "SPARKWING_RUN_HANDLE_FILE")
	if len(env) != 1 || env[0] != "PATH=/bin" {
		t.Fatalf("environment = %#v", env)
	}
}

func TestParseRunFlags_RetiredSwLocalOnlyIsUnknown(t *testing.T) {
	flags, passthroughArgs := parseRunFlags([]string{"--sw-local-only"})
	if flags.unknownRunnerFlag != "--sw-local-only" {
		t.Errorf("--sw-local-only should be refused as an unknown runner flag; got %q (passthrough %v)", flags.unknownRunnerFlag, passthroughArgs)
	}
}

func TestParseRunFlags_OnlyAndNoCacheCoexist(t *testing.T) {
	flags, _ := parseRunFlags([]string{"--sw-only=fictional-check-*", "--sw-no-cache"})
	if flags.only != "fictional-check-*" {
		t.Errorf("only = %q, want fictional-check-*", flags.only)
	}
	if !flags.noCache {
		t.Errorf("noCache: want true got false")
	}
}

func TestParseRunFlags_UnknownFlagsPassThrough(t *testing.T) {
	_, passthroughArgs := parseRunFlags([]string{"--sw-only=*", "--user-flag", "v", "--other"})
	expectedArguments := []string{"--user-flag", "v", "--other"}
	if !slices.Equal(passthroughArgs, expectedArguments) {
		t.Errorf("passthrough = %v, want %v", passthroughArgs, expectedArguments)
	}
}

func TestParseRunFlags_ProfileNeverReachesThePipeline(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--profile", "fictional-production"}, "fictional-production"},
		{"equals-form", []string{"--profile=fictional-production"}, "fictional-production"},
		{"trailing-flag", []string{"--profile"}, "<name>"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			flags, passthroughArgs := parseRunFlags(testCase.args)
			if flags.profile != testCase.want {
				t.Errorf("profile = %q, want %q", flags.profile, testCase.want)
			}
			if len(passthroughArgs) != 0 {
				t.Errorf("--profile reached the pipeline arguments: %v", passthroughArgs)
			}
		})
	}
}

func TestDispatchRun_RefusesProfileBeforeSideEffects(t *testing.T) {
	t.Setenv("SPARKWING_HOME", "")
	empty := t.TempDir()
	t.Chdir(empty)
	err := dispatchRun([]string{"deploy", "--profile", "prod"})
	if err == nil || !strings.Contains(err.Error(), "--profile is removed") ||
		!strings.Contains(err.Error(), "sparkwing pipeline trigger deploy --profile prod") {
		t.Fatalf("dispatch error = %v, want the removal and the pipeline trigger command", err)
	}
	if os.Getenv("SPARKWING_HOME") != "" {
		t.Error("refused --profile changed the environment")
	}
}

func TestParseRunFlags_RetiredSwProfileIsUnknown(t *testing.T) {
	flags, passthroughArgs := parseRunFlags([]string{"--sw-profile", "fictional-remote"})
	if flags.profile != "" {
		t.Errorf("--sw-profile should not set profile; got %q", flags.profile)
	}
	if flags.unknownRunnerFlag != "--sw-profile" {
		t.Errorf("--sw-profile should be refused as an unknown runner flag; got %q (passthrough %v)", flags.unknownRunnerFlag, passthroughArgs)
	}
}

func TestParseRunFlags_SeparatorPreservesPipelineArguments(t *testing.T) {
	want := []string{"--", "--sw-priority", "back", "--sw-no-cache", "--sw-mystery", "--profile", "fictional"}
	args := append([]string{"--sw-priority=front"}, want...)
	flags, passthroughArgs := parseRunFlags(args)
	if flags.priority != "front" || !flags.prioritySet || flags.noCache || flags.profile != "" {
		t.Fatalf("runner flags = %+v", flags)
	}
	if !slices.Equal(passthroughArgs, want) {
		t.Fatalf("pipeline arguments = %q, want %q", passthroughArgs, want)
	}
}

func TestDispatchRun_UnknownRunnerFlagPrecedesSideEffects(t *testing.T) {
	for _, unknown := range []string{"--sw-mystery", "--sw-mystery=value"} {
		t.Run(unknown, func(t *testing.T) {
			t.Setenv("SPARKWING_HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Chdir(t.TempDir())
			err := dispatchRun([]string{"fictional", unknown})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("unknown runner flag %q", unknown)) {
				t.Errorf("dispatch error = %v, want unknown runner flag %q", err, unknown)
			}
			if os.Getenv("SPARKWING_HOME") != "" || os.Getenv("XDG_CONFIG_HOME") != "" {
				t.Error("rejected runner flag changed the environment")
			}
		})
	}
}

func TestDispatchRun_SeparatorPassesRetiredFlagsToPipeline(t *testing.T) {
	empty := t.TempDir()
	t.Chdir(empty)
	err := dispatchRun([]string{"fictional", "--", "--sw-profile", "fictional"})
	if err == nil || !strings.Contains(err.Error(), "no .sparkwing/main.go or TypeScript .sparkwing/package.json found from "+empty) {
		t.Fatalf("dispatch error = %v, want pipeline directory lookup", err)
	}
}

func TestDispatchRunRejectsMalformedWorkersBeforeProjectLookup(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{"--sw-workers=four"}, {"--sw-workers=-2"}, {"--sw-workers", "four"}, {"--sw-workers", "-2"}, {"--sw-workers"}} {
		err := dispatchRun(append([]string{"missing-pipeline"}, args...))
		if err == nil || !strings.Contains(err.Error(), "--sw-workers must be a nonnegative integer") {
			t.Fatalf("%v error=%v", args, err)
		}
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

func TestParseRunFlags_OneOffSwitches(t *testing.T) {
	flags, rest := parseRunFlags([]string{"--sw-no-bincache", "--sw-hash-all-files", "--sw-allow-unadmitted", "--keep"})
	if !flags.noBincache || !flags.hashAllFiles || !flags.allowUnadmitted {
		t.Fatalf("flags = %+v, want every one-off switch set", flags)
	}
	if len(rest) != 1 || rest[0] != "--keep" {
		t.Fatalf("passthrough = %v, want only --keep", rest)
	}
}

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	flag "github.com/spf13/pflag"
)

// safety: pinned so adding or dropping -C on a verb is a deliberate edit. Membership is a
// judgement call, not a derived rule: every verb that resolves a storage profile reads the
// project config from the working directory, and most of those are not worth re-anchoring.
var chdirCommands = []string{
	"sparkwing examples scaffold",
	"sparkwing info",
	"sparkwing pipeline describe",
	"sparkwing pipeline discover",
	"sparkwing pipeline lint",
	"sparkwing pipeline list",
	"sparkwing pipeline new",
	"sparkwing pipeline run",
	"sparkwing run",
	"sparkwing runs annotations add",
	"sparkwing runs annotations list",
	"sparkwing runs approvals approve",
	"sparkwing runs approvals deny",
	"sparkwing runs approvals list",
	"sparkwing runs bounce",
	"sparkwing runs cancel",
	"sparkwing runs consumer start",
	"sparkwing runs consumer status",
	"sparkwing runs consumer stop",
	"sparkwing runs errors",
	"sparkwing runs failures",
	"sparkwing runs find",
	"sparkwing runs get",
	"sparkwing runs grep",
	"sparkwing runs last",
	"sparkwing runs list",
	"sparkwing runs logs",
	"sparkwing runs prune",
	"sparkwing runs receipt",
	"sparkwing runs retry",
	"sparkwing runs stats",
	"sparkwing runs status",
	"sparkwing runs summary",
	"sparkwing runs timeline",
	"sparkwing runs tree",
	"sparkwing runs triggers get",
	"sparkwing runs triggers list",
	"sparkwing runs wait",
}

func TestChdirFlagCoverageIsPinned(t *testing.T) {
	var got []string
	for _, cmd := range allCommands {
		if cmd.declaredFlags()["sw-cd"] {
			got = append(got, cmd.Path)
		}
	}
	slices.Sort(got)
	want := slices.Clone(chdirCommands)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("commands declaring --sw-cd drifted.\n got: %s\nwant: %s",
			strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
}

func TestEveryRunsVerbOffersChdir(t *testing.T) {
	declared := map[string]bool{}
	for _, path := range chdirCommands {
		declared[path] = true
	}
	for _, cmd := range allCommands {
		if !strings.HasPrefix(cmd.Path, "sparkwing runs ") || len(cmd.Flags) == 0 {
			continue
		}
		if !declared[cmd.Path] {
			t.Errorf("%s reads a profile resolved from the working directory but offers no -C/--sw-cd", cmd.Path)
		}
	}
}

func TestParseAndCheckReAnchorsDeclaredChdir(t *testing.T) {
	start := t.TempDir()
	t.Chdir(start)
	target := filepath.Join(start, "elsewhere")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	fs := flag.NewFlagSet(cmdJobsStatus.Path, flag.ContinueOnError)
	fs.String("run", "", "run identifier")
	if err := parseAndCheck(cmdJobsStatus, fs, []string{"--run", "run-x", "--sw-cd", target}); err != nil {
		t.Fatalf("parseAndCheck: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("eval cwd: %v", err)
	}
	wantDir, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("eval target: %v", err)
	}
	if gotDir != wantDir {
		t.Errorf("cwd = %q, want %q", gotDir, wantDir)
	}
}

func TestParseAndCheckRejectsUndeclaredChdir(t *testing.T) {
	fs := flag.NewFlagSet(cmdVersion.Path, flag.ContinueOnError)
	err := parseAndCheck(cmdVersion, fs, []string{"--sw-cd", t.TempDir()})
	if err == nil {
		t.Fatal("expected --sw-cd to be rejected on a command that does not declare it")
	}
}

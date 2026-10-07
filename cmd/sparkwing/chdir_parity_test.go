package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	flag "github.com/spf13/pflag"
)

func TestRootChdirReAnchorsBeforeTheVerb(t *testing.T) {
	start := t.TempDir()
	t.Chdir(start)
	target := filepath.Join(start, "elsewhere")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, form := range [][]string{{"-C", target}, {"-C=" + target}, {"-C" + target}} {
		t.Chdir(start)
		args, err := moveRootFlags(append(form, "runs", "list"))
		if err != nil {
			t.Fatalf("%q: %v", form, err)
		}
		if strings.Join(args, " ") != "runs list" {
			t.Errorf("%q: args = %q, want the verb alone", form, args)
		}
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := filepath.EvalSymlinks(cwd)
		want, _ := filepath.EvalSymlinks(target)
		if got != want {
			t.Errorf("%q: cwd = %q, want %q", form, got, want)
		}
	}
}

func TestRootChdirToAMissingDirectoryFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := moveRootFlags([]string{"-C", missing, "pipeline", "list"}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v, want it to name %s", err, missing)
	}
}

func TestRootProfileMovesAfterTheVerb(t *testing.T) {
	args, err := moveRootFlags([]string{"--profile", "prod", "runs", "status", "run-x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "runs status --profile prod run-x" {
		t.Errorf("args = %q", got)
	}
	args, err = moveRootFlags([]string{"--profile=prod", "run", "build", "--sw-dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "run build --profile=prod --sw-dry-run" {
		t.Errorf("args = %q", got)
	}
}

func TestChdirAfterTheVerbIsRefused(t *testing.T) {
	fs := flag.NewFlagSet(cmdJobsStatus.Path, flag.ContinueOnError)
	fs.String("run", "", "run identifier")
	if err := parseAndCheck(cmdJobsStatus, fs, []string{"--run", "run-x", "-C", t.TempDir()}); err == nil {
		t.Fatal("-C after the verb was accepted; it belongs before the verb")
	}
}

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
)

func TestDispatchGitBindingsAreClearedBeforeHelpAndCheckoutProperty(t *testing.T) {
	bindings := []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_PREFIX", "GIT_COMMON_DIR",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_QUARANTINE_PATH",
	}
	dir := t.TempDir()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	for _, name := range append(bindings, gitenv.GateIndexVar, "GIT_AUTHOR_NAME") {
		t.Setenv(name, "")
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	property := func(seed uint64, help bool) bool {
		value := fmt.Sprintf("captured-%x", seed)
		for _, name := range bindings {
			if err := os.Setenv(name, value); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Setenv("GIT_AUTHOR_NAME", value); err != nil {
			t.Fatal(err)
		}
		args := []string{"--checkout", dir, "run", "missing"}
		if help {
			args = []string{"run", "--help"}
		}
		captureStdout(t, func() { _ = runSparkwing(args) })
		for _, name := range bindings {
			if got, present := os.LookupEnv(name); present {
				t.Logf("%s remained bound to %q", name, got)
				return false
			}
		}
		if got := os.Getenv(gitenv.GateIndexVar); got != filepath.Join(original, value) {
			t.Logf("gate index = %q, want launch-relative %q", got, filepath.Join(original, value))
			return false
		}
		return os.Getenv("GIT_AUTHOR_NAME") == value
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

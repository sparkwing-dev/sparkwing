package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSurfaceDispatchRejectsBeforeState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", filepath.Join(home, "absent"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	for _, command := range []string{
		"cluster", "cloud", "configure", "serve", "crons", "runs consumer", "runs bounce", "debug rerun",
		"runs status r1", "runs list --limit 0", "runs list --node j1", "run check --unknown",
		"pipeline new --pipeline sample -o yaml", "pipeline new -o json",
		"pipeline hooks install --fleet", "pipeline hooks status --all",
		"pipeline hooks uninstall --output=", "cache info --all",
		"run check --profile prod", "run check --sw-no-cache", "run check --sw-allow destructive",
	} {
		err := runSparkwing(strings.Fields(command))
		if err == nil || exitCodeFor(err) != 2 {
			t.Errorf("%s: error=%v exit=%d", command, err, exitCodeFor(err))
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected commands created state: %v %v", entries, err)
	}
}

func TestSurfaceDispatchHelp(t *testing.T) {
	for _, command := range []string{
		"--profile prod pipeline trigger --help", "pipeline trigger --profile prod --help",
		"dashboard start --help", "schedules pause --help", "profiles add --help",
		"runners list --help", "pools allow --help", "operator tokens create --help",
	} {
		if err := runSparkwing(strings.Fields(command)); err != nil {
			t.Errorf("%s: %v", command, err)
		}
	}
}

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
		{"--target=prod"},
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
	flags, _ := parseRunFlagsArgs([]string{"--publish=ci", "--sw-cache-from=ci"})
	if flags.parseErr != nil || flags.publish != "ci" || flags.cacheFrom != "ci" {
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

func TestLogFormatRefusalNamesTheValueAndTheFix(t *testing.T) {
	t.Setenv("SPARKWING_LOG_FORMAT", "quiet")
	flags, _ := parseRunFlagsArgs(nil)
	if flags.parseErr == nil {
		t.Fatal("SPARKWING_LOG_FORMAT=quiet was accepted")
	}
	for _, want := range []string{`SPARKWING_LOG_FORMAT="quiet"`, "sparkwing pipeline hooks install"} {
		if !strings.Contains(flags.parseErr.Error(), want) {
			t.Errorf("refusal %q does not contain %q", flags.parseErr, want)
		}
	}

}

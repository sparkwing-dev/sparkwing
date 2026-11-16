package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseRunFlagsFinalGrammar(t *testing.T) {
	flags, args := parseRunFlagsArgs([]string{"--only=build-*", "--no-memo", "--allow-risk", "deploy,prod", "--pause-before", "build,test", "--pause-after=publish", "--pause-on-failure=false", "--run-handle-file=/tmp/result", "--", "--sw-no-cache", "--", "value"})
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

func TestRunFlagsReadEveryUnprefixedName(t *testing.T) {
	foreground, _ := parseRunFlagsArgs([]string{
		"--route", "cloud", "--profile", "prod", "--cache-from", "cache", "--ref", "main",
		"--workers", "3", "--verbose", "--start-at", "build", "--stop-at", "test", "--no-memo", "--priority", "front",
		"--no-update", "--no-bincache", "--hash-all-files", "--allow-unadmitted", "--dry-run", "--allow-risk", "prod",
		"--index", "/tmp/index", "--run-handle-file", "/tmp/handle", "--pause-before", "a", "--pause-after", "b", "--pause-on-failure",
	})
	if foreground.parseErr != nil {
		t.Fatal(foreground.parseErr)
	}
	f := foreground
	if f.route != "cloud" || f.cacheFrom != "cache" || f.ref != "main" || f.workers != 3 || !f.verbose ||
		f.startAt != "build" || f.stopAt != "test" || !f.noCache || f.priority != "front" || !f.prioritySet ||
		!f.noUpdate || !f.noBincache || !f.hashAllFiles || !f.allowUnadmitted || !f.dryRun ||
		!slices.Equal(f.allow, []string{"prod"}) || f.index != "/tmp/index" || f.runHandleFile != "/tmp/handle" ||
		!slices.Equal(f.pauseBefore, []string{"a"}) || !slices.Equal(f.pauseAfter, []string{"b"}) || f.pauseOnFailure == nil || !*f.pauseOnFailure {
		t.Fatalf("foreground flags: %#v", f)
	}
	detached, _ := parseRunFlagsArgs([]string{"--detach", "--pipeline-ref", "abc", "--idempotency-key", "k", "--request-id", "r", "--only", "build-*"})
	if detached.parseErr != nil {
		t.Fatal(detached.parseErr)
	}
	if d := detached; !d.detached || d.pipelineRef != "abc" || d.idempotencyKey != "k" || d.requestID != "r" || d.only != "build-*" {
		t.Fatalf("detached flags: %#v", d)
	}
}

func TestParseRunFlagsRejectsInvalidBeforeExecution(t *testing.T) {
	for _, args := range [][]string{
		{"--only"},
		{"--index"},
		{"--workers=0"},
		{"--workers=-1"},
		{"--workers=x"},
		{"--sw-no-cache"},
		{"--sw-allow=prod"},
		{"--profile=prod"},
		{"--sw-profile=prod"},
		{"--unknown"},
		{"--target=prod"},
		{"--detach", "--workers=1"},
		{"--request-id=id"},
		{"--sw-consumer-idle=1m"},
	} {
		flags, args := parseRunFlagsArgs(args)
		if flags.parseErr == nil || exitCodeFor(flags.parseErr) != 2 || len(args) != 0 {
			t.Fatalf("accepted invalid args: %#v %v", flags, args)
		}
	}
}

func TestParseRunFlagsRetainsPublicationSelection(t *testing.T) {
	flags, _ := parseRunFlagsArgs([]string{"--publish=ci", "--cache-from=ci"})
	if flags.parseErr != nil || flags.publish != "ci" || flags.cacheFrom != "ci" {
		t.Fatalf("selection lost: %#v", flags)
	}
}

func TestDispatchRunValidationPrecedesSideEffects(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("SPARKWING_HOME", home)
	for _, args := range [][]string{{"build", "--profile=prod"}, {"build", "--workers=0"}, {"build", "--unknown"}} {
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
	for _, want := range []string{`SPARKWING_LOG_FORMAT="quiet"`, "must be pretty, json or plain"} {
		if !strings.Contains(flags.parseErr.Error(), want) {
			t.Errorf("refusal %q does not contain %q", flags.parseErr, want)
		}
	}
}

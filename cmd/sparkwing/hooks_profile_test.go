package main

import (
	"strings"
	"testing"
)

// A hook must not take its store from the shell that invoked git.
// Leaving SPARKWING_PROFILE inherited put two identical commits
// seconds apart into different stores, both reporting a green tick.
func TestRenderHookScript_ScrubsAmbientProfile(t *testing.T) {
	script := renderHookScript("pre-commit", []string{"gate"}, false, "")
	if !strings.Contains(script, "unset SPARKWING_PROFILE") {
		t.Errorf("hook should scrub the ambient profile:\n%s", script)
	}
	if strings.Contains(script, "--profile") {
		t.Errorf("no --profile was pinned, so none should be passed:\n%s", script)
	}
}

// --profile at install time is pinned into the script, so the gate's
// store is readable in the hook file rather than inferred from the
// environment of whoever ran git.
func TestRenderHookScript_PinsInstalledProfile(t *testing.T) {
	script := renderHookScript("pre-commit", []string{"gate", "lint"}, false, "bucket")
	if got := strings.Count(script, "--profile 'bucket'"); got != 2 {
		t.Errorf("each pipeline should carry the pinned profile; got %d occurrences:\n%s", got, script)
	}
	if !strings.Contains(script, "unset SPARKWING_PROFILE") {
		t.Errorf("the pin must still scrub the inherited value:\n%s", script)
	}
}

// A name carrying shell syntax reaches sparkwing as the name typed.
func TestRenderHookScript_QuotesProfileName(t *testing.T) {
	script := renderHookScript("pre-commit", []string{"gate"}, false, "a b; rm -rf /")
	if !strings.Contains(script, `--profile 'a b; rm -rf /'`) {
		t.Errorf("profile name should be single-quoted:\n%s", script)
	}
}

// A forwarder-only hook runs no pipeline, so there is nothing to pin
// and nothing to scrub.
func TestRenderHookScript_ForwarderCarriesNoProfile(t *testing.T) {
	script := renderHookScript("prepare-commit-msg", nil, true, "bucket")
	if strings.Contains(script, "--profile") {
		t.Errorf("a forwarder should carry no profile:\n%s", script)
	}
}

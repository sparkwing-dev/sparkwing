package main

import (
	"strings"
	"testing"
)

func TestRenderHookScript_ScrubsAmbientProfile(t *testing.T) {
	script := renderHookScript("pre-commit", []string{"gate"}, false, "")
	if !strings.Contains(script, "unset SPARKWING_PROFILE") {
		t.Errorf("hook should scrub the ambient profile:\n%s", script)
	}
	if strings.Contains(script, "--profile") {
		t.Errorf("no --profile was pinned, so none should be passed:\n%s", script)
	}
}

func TestRenderHookScript_PinsInstalledProfile(t *testing.T) {
	script := renderHookScript("pre-commit", []string{"gate", "lint"}, false, "bucket")
	if got := strings.Count(script, "--profile 'bucket'"); got != 2 {
		t.Errorf("each pipeline should carry the pinned profile; got %d occurrences:\n%s", got, script)
	}
	if !strings.Contains(script, "unset SPARKWING_PROFILE") {
		t.Errorf("the pin must still scrub the inherited value:\n%s", script)
	}
}

func TestRenderHookScript_QuotesProfileName(t *testing.T) {
	script := renderHookScript("pre-commit", []string{"gate"}, false, "a b; rm -rf /")
	if !strings.Contains(script, `--profile 'a b; rm -rf /'`) {
		t.Errorf("profile name should be single-quoted:\n%s", script)
	}
}

func TestRenderHookScript_ForwarderCarriesNoProfile(t *testing.T) {
	script := renderHookScript("prepare-commit-msg", nil, true, "bucket")
	if strings.Contains(script, "--profile") {
		t.Errorf("a forwarder should carry no profile:\n%s", script)
	}
}

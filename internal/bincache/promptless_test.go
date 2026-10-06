package bincache

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPromptlessGitPreservesStoredCredentials(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GCM_INTERACTIVE", "1")
	t.Setenv("GIT_ASKPASS", "must-not-run")
	env := gitHTTPEnv("http://cache.invalid", "")
	for key, want := range map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
		"GCM_INTERACTIVE":     "0",
		"GIT_ASKPASS":         "",
	} {
		var got string
		for _, entry := range env {
			if strings.HasPrefix(entry, key+"=") {
				got = strings.TrimPrefix(entry, key+"=")
			}
		}
		if got != want {
			t.Fatalf("%s interactive override was not fenced", key)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "credential.helper=", "-c",
		"credential.helper=!f() { printf 'username=fixture-user\\npassword=fixture-pass\\n'; }; f",
		"credential", "fill")
	cmd.Env = env
	cmd.Stdin = strings.NewReader("protocol=https\nhost=fixture.invalid\n\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("stored credential lookup failed")
	}
	if !strings.Contains(string(out), "username=fixture-user\n") || !strings.Contains(string(out), "password=fixture-pass\n") {
		t.Fatal("stored credential lookup did not retain the configured helper")
	}
}

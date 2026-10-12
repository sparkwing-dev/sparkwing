package bincache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPromptlessGitPreservesStoredCredentials(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GCM_INTERACTIVE", "1")
	askpass := filepath.Join(t.TempDir(), "askpass.sh")
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\necho fixture-askpass\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_ASKPASS", askpass)
	env := gitHTTPEnv("http://cache.invalid", "")
	wantGCM := "1"
	if runtime.GOOS == "windows" {
		wantGCM = "0"
	}
	for key, want := range map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
		"GCM_INTERACTIVE":     wantGCM,
	} {
		var got string
		for _, entry := range env {
			if strings.HasPrefix(entry, key+"=") {
				got = strings.TrimPrefix(entry, key+"=")
			}
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	for _, tc := range []struct {
		name, helper, want string
	}{
		{"stored helper", "!f() { printf 'username=fixture-user\\npassword=fixture-pass\\n'; }; f", "fixture-user"},
		{"noninteractive askpass", "", "fixture-askpass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			args := []string{"-c", "credential.helper="}
			if tc.helper != "" {
				args = append(args, "-c", "credential.helper="+tc.helper)
			}
			cmd := exec.CommandContext(ctx, "git", append(args, "credential", "fill")...)
			cmd.Env = env
			cmd.Stdin = strings.NewReader("protocol=https\nhost=fixture.invalid\n\n")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("credential lookup failed: %v", err)
			}
			if !strings.Contains(string(out), "username="+tc.want+"\n") {
				t.Fatalf("credential lookup did not use the %s:\n%s", tc.name, out)
			}
		})
	}
}

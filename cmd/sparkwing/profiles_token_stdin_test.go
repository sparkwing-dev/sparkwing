package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

func withStdin(t *testing.T, body string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(body); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	original := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = original
		_ = r.Close()
	})
}

func profilesFixturePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	t.Setenv("SPARKWING_PROFILES", path)
	return path
}

func loadSavedProfile(t *testing.T, path, name string) *profile.Profile {
	t.Helper()
	cfg, err := profile.Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		t.Fatalf("profile %q missing from %s", name, path)
	}
	return p
}

func TestReadTokenStdinTrimsPipedInput(t *testing.T) {
	withStdin(t, "swu_piped\n")
	got, err := readTokenStdin(os.Stdin, os.Stderr, "bearer token")
	if err != nil {
		t.Fatalf("readTokenStdin: %v", err)
	}
	if got != "swu_piped" {
		t.Errorf("readTokenStdin = %q, want %q", got, "swu_piped")
	}
}

func TestProfilesAddReadsTokenFromStdin(t *testing.T) {
	path := profilesFixturePath(t)
	withStdin(t, "swu_from_stdin\n")
	out := captureStdout(t, func() {
		args := []string{"--name", "prod", "--controller", "https://api.example.dev", "--token-stdin"}
		if err := runProfilesAdd(args); err != nil {
			t.Errorf("profiles add: %v", err)
		}
	})
	if strings.Contains(out, "swu_from_stdin") {
		t.Errorf("stdout echoed the token:\n%s", out)
	}
	if got := loadSavedProfile(t, path, "prod").ControllerToken(); got != "swu_from_stdin" {
		t.Errorf("stored token = %q, want %q", got, "swu_from_stdin")
	}
}

func TestProfilesSetReadsTokenFromStdin(t *testing.T) {
	path := profilesFixturePath(t)
	withStdin(t, "swu_first\n")
	captureStdout(t, func() {
		args := []string{"--name", "prod", "--controller", "https://api.example.dev", "--token-stdin"}
		if err := runProfilesAdd(args); err != nil {
			t.Fatalf("profiles add: %v", err)
		}
	})
	withStdin(t, "swu_rotated\n")
	captureStdout(t, func() {
		if err := runProfilesSet([]string{"--name", "prod", "--token-stdin"}); err != nil {
			t.Fatalf("profiles set: %v", err)
		}
	})
	if got := loadSavedProfile(t, path, "prod").ControllerToken(); got != "swu_rotated" {
		t.Errorf("stored token = %q, want %q", got, "swu_rotated")
	}
}

func TestProfilesTokenFlagsConflict(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func([]string) error
		args []string
	}{
		{"add", runProfilesAdd, []string{"--name", "prod", "--controller", "https://api.example.dev", "--token", "swu_x", "--token-stdin"}},
		{"set", runProfilesSet, []string{"--name", "prod", "--token", "swu_x", "--token-stdin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profilesFixturePath(t)
			withStdin(t, "swu_from_stdin\n")
			err := tc.run(tc.args)
			if err == nil {
				t.Fatal("expected --token with --token-stdin to be rejected")
			}
			if !strings.Contains(err.Error(), "cannot be used together") {
				t.Errorf("error = %v, want a conflict about --token and --token-stdin", err)
			}
		})
	}
}

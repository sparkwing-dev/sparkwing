package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fleet"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

func TestParseRunFlags_Only(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--sw-only", "test-phase-*"}, "test-phase-*"},
		{"equals-form", []string{"--sw-only=test-phase-*"}, "test-phase-*"},
		{"empty-trailing-flag-falls-through", []string{"--sw-only"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, pass := parseRunFlags(tc.args)
			if wf.only != tc.want {
				t.Errorf("only = %q, want %q", wf.only, tc.want)
			}
			if tc.want == "" && !slices.Contains(pass, "--sw-only") {
				t.Errorf("incomplete --sw-only should pass through; got passthrough=%v", pass)
			}
		})
	}
}

func TestParseRunFlags_NoCache(t *testing.T) {
	wf, pass := parseRunFlags([]string{"--sw-no-cache"})
	if !wf.noCache {
		t.Errorf("noCache: want true got false")
	}
	if len(pass) != 0 {
		t.Errorf("passthrough should be empty, got %v", pass)
	}
}

func TestParseRunFlags_RunHandleFile(t *testing.T) {
	wf, pass := parseRunFlags([]string{"--sw-run-handle-file", "/tmp/run.json", "--target", "prod"})
	if wf.runHandleFile != "/tmp/run.json" {
		t.Fatalf("runHandleFile = %q", wf.runHandleFile)
	}
	if got := strings.Join(pass, " "); got != "--target prod" {
		t.Fatalf("passthrough = %q", got)
	}
}

func TestRemoveEnvDropsAmbientRunHandle(t *testing.T) {
	env := removeEnv([]string{"PATH=/bin", "SPARKWING_RUN_HANDLE_FILE=/tmp/stale"}, "SPARKWING_RUN_HANDLE_FILE")
	if len(env) != 1 || env[0] != "PATH=/bin" {
		t.Fatalf("environment = %#v", env)
	}
}

func TestParseRunFlags_LocalOnly(t *testing.T) {
	wf, pass := parseRunFlags([]string{"--sw-local-only"})
	if !wf.localOnly {
		t.Errorf("localOnly: want true got false")
	}
	if len(pass) != 0 {
		t.Errorf("passthrough should be empty, got %v", pass)
	}
}

func TestParseRunFlags_FleetAndLocalOnlyCoexist(t *testing.T) {
	wf, pass := parseRunFlags([]string{"--sw-fleet", "--sw-local-only"})
	if !wf.fleet || !wf.localOnly {
		t.Fatalf("flags = %+v", wf)
	}
	if len(pass) != 0 {
		t.Fatalf("passthrough = %v", pass)
	}
}

func TestDispatchFleetMissingConfigNamesSetupCommand(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_FLEET_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := dispatchRun([]string{"missing", "--sw-fleet", "--sw-cd", repo})
	if err == nil || !strings.Contains(err.Error(), "sparkwing fleet init") {
		t.Fatalf("missing config error = %v", err)
	}
}

func TestDispatchFleetEmptyConfigNamesEnrollmentCommand(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := fleet.Create(configPath, fleet.Config{
		Listen: "127.0.0.1:7443", PublicURL: "http://127.0.0.1:7443",
		Local: fleet.Local{MaxConcurrent: 1, Contribution: "50%,50%"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_FLEET_CONFIG", configPath)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := dispatchRun([]string{"missing", "--sw-fleet", "--sw-cd", repo})
	if err == nil || !strings.Contains(err.Error(), "sparkwing fleet agents enroll") {
		t.Fatalf("empty config error = %v", err)
	}
}

func TestParseRunFlags_OnlyAndNoCacheCoexist(t *testing.T) {
	wf, _ := parseRunFlags([]string{"--sw-only=lint-*", "--sw-no-cache"})
	if wf.only != "lint-*" {
		t.Errorf("only = %q, want lint-*", wf.only)
	}
	if !wf.noCache {
		t.Errorf("noCache: want true got false")
	}
}

func TestParseRunFlags_UnknownFlagsPassThrough(t *testing.T) {
	_, pass := parseRunFlags([]string{"--sw-only=*", "--user-flag", "v", "--other"})
	wantPass := []string{"--user-flag", "v", "--other"}
	if !slices.Equal(pass, wantPass) {
		t.Errorf("passthrough = %v, want %v", pass, wantPass)
	}
}

func TestParseRunFlags_Profile(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--profile", "prod"}, "prod"},
		{"equals-form", []string{"--profile=prod"}, "prod"},
		{"empty-trailing-flag-falls-through", []string{"--profile"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, pass := parseRunFlags(tc.args)
			if wf.profile != tc.want {
				t.Errorf("profile = %q, want %q", wf.profile, tc.want)
			}
			if tc.want == "" && !slices.Contains(pass, "--profile") {
				t.Errorf("incomplete --profile should pass through; got passthrough=%v", pass)
			}
		})
	}
}

func TestParseRunFlags_ProfileSetTargetFallsThrough(t *testing.T) {
	wf, pass := parseRunFlags([]string{"--profile", "local", "--target", "prod"})
	if wf.profile != "local" {
		t.Errorf("profile = %q, want local", wf.profile)
	}
	if !slices.Contains(pass, "--target") || !slices.Contains(pass, "prod") {
		t.Errorf("--target should fall through to pipeline args; passthrough=%v", pass)
	}
}

func TestParseRunFlags_RetiredSwProfileFallsThrough(t *testing.T) {
	wf, pass := parseRunFlags([]string{"--sw-profile", "remote"})
	if wf.profile != "" {
		t.Errorf("--sw-profile should not set profile; got %q", wf.profile)
	}
	if err := checkRetiredWhereFlags(pass, nil); err == nil || !strings.Contains(err.Error(), "--sw-profile") {
		t.Errorf("checkRetiredWhereFlags: want --sw-profile pointer, got %v", err)
	}
}

func TestRetiredFlagYieldsToTheCommandThatDeclaresIt(t *testing.T) {
	args := []string{"--name", "x", "--on", "pull_request"}
	if err := checkRetiredWhereFlags(args, map[string]bool{"on": true}); err != nil {
		t.Errorf("a command declaring --on still hit the retired-flag guard: %v", err)
	}
	if err := checkRetiredWhereFlags(args, map[string]bool{"name": true}); err == nil {
		t.Error("--on passed the guard on a command that does not declare it")
	}
	if err := checkRetiredWhereFlags([]string{"--on=prod"}, nil); err == nil {
		t.Error("--on=value form escaped the guard")
	}
}

func TestParseRunFlags_IsolatedHome(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--sw-isolated-home", "/tmp/gate"}, "/tmp/gate"},
		{"equals-form", []string{"--sw-isolated-home=/tmp/gate"}, "/tmp/gate"},
		{"empty-trailing-flag-falls-through", []string{"--sw-isolated-home"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, pass := parseRunFlags(tc.args)
			if wf.isolatedHome != tc.want {
				t.Errorf("isolatedHome = %q, want %q", wf.isolatedHome, tc.want)
			}
			if tc.want == "" && !slices.Contains(pass, "--sw-isolated-home") {
				t.Errorf("incomplete --sw-isolated-home should pass through; got passthrough=%v", pass)
			}
			if tc.want != "" && len(pass) != 0 {
				t.Errorf("passthrough should be empty, got %v", pass)
			}
		})
	}
}

func TestApplyIsolatedHomeMovesStateAndConfigResolution(t *testing.T) {
	t.Setenv("SPARKWING_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	root := filepath.Join(t.TempDir(), "gate")

	if err := applyIsolatedHome(root); err != nil {
		t.Fatalf("applyIsolatedHome(%s): %v", root, err)
	}

	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if p.Root != root {
		t.Errorf("state home = %q, want %q", p.Root, root)
	}
	config, err := fssecure.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if want := filepath.Join(root, "config", "sparkwing"); config != want {
		t.Errorf("config dir = %q, want %q", config, want)
	}
	for _, dir := range []string{root, isolatedHomeConfigDir(root)} {
		info, statErr := os.Stat(dir)
		if statErr != nil {
			t.Fatalf("stat %s: %v", dir, statErr)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s mode = %04o, want no group or other access", dir, perm)
		}
	}
}

func TestApplyIsolatedHomeReportsADirectoryItCannotPrepare(t *testing.T) {
	t.Setenv("SPARKWING_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	err := applyIsolatedHome(file)
	if err == nil {
		t.Fatal("applyIsolatedHome accepted a path that is a file")
	}
	if !strings.Contains(err.Error(), "--sw-isolated-home") {
		t.Errorf("error = %q, want it to name the flag", err)
	}
}

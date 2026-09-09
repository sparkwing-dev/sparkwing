package main

import (
	"encoding/json"
	"fmt"
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
		{"space-separated", []string{"--sw-only", "fictional-stage-*"}, "fictional-stage-*"},
		{"equals-form", []string{"--sw-only=fictional-stage-*"}, "fictional-stage-*"},
		{"empty-trailing-flag-falls-through", []string{"--sw-only"}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			flags, passthroughArgs := parseRunFlags(testCase.args)
			if flags.only != testCase.want {
				t.Errorf("only = %q, want %q", flags.only, testCase.want)
			}
			if testCase.want == "" && !slices.Contains(passthroughArgs, "--sw-only") {
				t.Errorf("incomplete --sw-only should pass through; got passthrough=%v", passthroughArgs)
			}
		})
	}
}

func TestParseRunFlags_NoCache(t *testing.T) {
	flags, passthroughArgs := parseRunFlags([]string{"--sw-no-cache"})
	if !flags.noCache {
		t.Errorf("noCache: want true got false")
	}
	if len(passthroughArgs) != 0 {
		t.Errorf("passthrough should be empty, got %v", passthroughArgs)
	}
}

func TestParseRunFlags_RunHandleFile(t *testing.T) {
	flags, passthroughArgs := parseRunFlags([]string{"--sw-run-handle-file", "/tmp/fictional-run.json", "--target", "fictional-production"})
	if flags.runHandleFile != "/tmp/fictional-run.json" {
		t.Fatalf("runHandleFile = %q", flags.runHandleFile)
	}
	if got := strings.Join(passthroughArgs, " "); got != "--target fictional-production" {
		t.Fatalf("passthrough = %q", got)
	}
}

func TestRemoveEnvDropsAmbientRunHandle(t *testing.T) {
	env := removeEnv([]string{"PATH=/bin", "SPARKWING_RUN_HANDLE_FILE=/tmp/fictional-stale"}, "SPARKWING_RUN_HANDLE_FILE")
	if len(env) != 1 || env[0] != "PATH=/bin" {
		t.Fatalf("environment = %#v", env)
	}
}

func TestParseRunFlags_LocalOnly(t *testing.T) {
	flags, passthroughArgs := parseRunFlags([]string{"--sw-local-only"})
	if !flags.localOnly {
		t.Errorf("localOnly: want true got false")
	}
	if len(passthroughArgs) != 0 {
		t.Errorf("passthrough should be empty, got %v", passthroughArgs)
	}
}

func TestParseRunFlags_FleetAndLocalOnlyCoexist(t *testing.T) {
	flags, passthroughArgs := parseRunFlags([]string{"--sw-fleet", "--sw-local-only"})
	if !flags.fleet || !flags.localOnly {
		t.Fatalf("flags = %+v", flags)
	}
	if len(passthroughArgs) != 0 {
		t.Fatalf("passthrough = %v", passthroughArgs)
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
	flags, _ := parseRunFlags([]string{"--sw-only=fictional-check-*", "--sw-no-cache"})
	if flags.only != "fictional-check-*" {
		t.Errorf("only = %q, want fictional-check-*", flags.only)
	}
	if !flags.noCache {
		t.Errorf("noCache: want true got false")
	}
}

func TestParseRunFlags_UnknownFlagsPassThrough(t *testing.T) {
	_, passthroughArgs := parseRunFlags([]string{"--sw-only=*", "--user-flag", "v", "--other"})
	expectedArguments := []string{"--user-flag", "v", "--other"}
	if !slices.Equal(passthroughArgs, expectedArguments) {
		t.Errorf("passthrough = %v, want %v", passthroughArgs, expectedArguments)
	}
}

func TestParseRunFlags_Profile(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--profile", "fictional-production"}, "fictional-production"},
		{"equals-form", []string{"--profile=fictional-production"}, "fictional-production"},
		{"empty-trailing-flag-falls-through", []string{"--profile"}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			flags, passthroughArgs := parseRunFlags(testCase.args)
			if flags.profile != testCase.want {
				t.Errorf("profile = %q, want %q", flags.profile, testCase.want)
			}
			if testCase.want == "" && !slices.Contains(passthroughArgs, "--profile") {
				t.Errorf("incomplete --profile should pass through; got passthrough=%v", passthroughArgs)
			}
		})
	}
}

func TestParseRunFlags_ProfileSetTargetFallsThrough(t *testing.T) {
	flags, passthroughArgs := parseRunFlags([]string{"--profile", "fictional-local", "--target", "fictional-production"})
	if flags.profile != "fictional-local" {
		t.Errorf("profile = %q, want fictional-local", flags.profile)
	}
	if !slices.Contains(passthroughArgs, "--target") || !slices.Contains(passthroughArgs, "fictional-production") {
		t.Errorf("--target should fall through to pipeline args; passthrough=%v", passthroughArgs)
	}
}

func TestParseRunFlags_RetiredSwProfileFallsThrough(t *testing.T) {
	flags, passthroughArgs := parseRunFlags([]string{"--sw-profile", "fictional-remote"})
	if flags.profile != "" {
		t.Errorf("--sw-profile should not set profile; got %q", flags.profile)
	}
	if err := checkRetiredWhereFlags(passthroughArgs, nil); err == nil || !strings.Contains(err.Error(), "--sw-profile") {
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
	if err := checkRetiredWhereFlags([]string{"--on=fictional-production"}, nil); err == nil {
		t.Error("--on=value form escaped the guard")
	}
}

func TestParseRunFlags_IsolatedHome(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space-separated", []string{"--sw-isolated-home", "/tmp/fictional-gate"}, "/tmp/fictional-gate"},
		{"equals-form", []string{"--sw-isolated-home=/tmp/fictional-gate"}, "/tmp/fictional-gate"},
		{"empty-trailing-flag-falls-through", []string{"--sw-isolated-home"}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			flags, passthroughArgs := parseRunFlags(testCase.args)
			if flags.isolatedHome != testCase.want {
				t.Errorf("isolatedHome = %q, want %q", flags.isolatedHome, testCase.want)
			}
			if testCase.want == "" && !slices.Contains(passthroughArgs, "--sw-isolated-home") {
				t.Errorf("incomplete --sw-isolated-home should pass through; got passthrough=%v", passthroughArgs)
			}
			if testCase.want != "" && len(passthroughArgs) != 0 {
				t.Errorf("passthrough should be empty, got %v", passthroughArgs)
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

func TestParseRunFlags_SeparatorPreservesPipelineArguments(t *testing.T) {
	want := []string{"--", "--sw-priority", "back", "--sw-no-cache", "--sw-mystery", "--profile", "fictional"}
	args := append([]string{"--sw-priority=front"}, want...)
	flags, passthroughArgs := parseRunFlags(args)
	if flags.priority != "front" || !flags.prioritySet || flags.noCache || flags.profile != "" {
		t.Fatalf("runner flags = %+v", flags)
	}
	if !slices.Equal(passthroughArgs, want) {
		t.Fatalf("pipeline arguments = %q, want %q", passthroughArgs, want)
	}
}

func TestDispatchRun_UnknownRunnerFlagPrecedesSideEffects(t *testing.T) {
	for _, unknown := range []string{"--sw-mystery", "--sw-mystery=value"} {
		t.Run(unknown, func(t *testing.T) {
			t.Setenv("SPARKWING_HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			home := filepath.Join(t.TempDir(), "home")
			missing := filepath.Join(t.TempDir(), "missing")
			err := dispatchRun([]string{"fictional", "--sw-isolated-home", home, "--sw-cd", missing, unknown})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("unknown runner flag %q", unknown)) {
				t.Errorf("dispatch error = %v, want unknown runner flag %q", err, unknown)
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Errorf("isolated home stat = %v, want no directory", err)
			}
			if os.Getenv("SPARKWING_HOME") != "" || os.Getenv("XDG_CONFIG_HOME") != "" {
				t.Error("rejected runner flag changed the environment")
			}
		})
	}
}

func TestDispatchRun_RetiredRunnerFlagKeepsMigrationHint(t *testing.T) {
	err := dispatchRun([]string{"fictional", "--sw-profile", "fictional"})
	if err == nil || !strings.Contains(err.Error(), "--profile") || strings.Contains(err.Error(), "unknown runner flag") {
		t.Fatalf("dispatch error = %v, want retired profile flag migration", err)
	}
}

func TestDispatchRun_SeparatorPassesRetiredFlagsToPipeline(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	err := dispatchRun([]string{"fictional", "--sw-cd", missing, "--", "--sw-profile", "fictional"})
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("dispatch error = %v, want pipeline directory lookup", err)
	}
}

func TestDispatchRun_ConsumesSeparatorBeforeExecutingPipeline(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SPARKWING_NO_BINCACHE", "1")
	t.Setenv("SPARKWING_NO_AUTO_REGISTER", "1")
	t.Setenv("GOWORK", "off")
	repository := t.TempDir()
	pipelineDirectory := filepath.Join(repository, ".sparkwing")
	if err := os.Mkdir(pipelineDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	argumentsFile := filepath.Join(t.TempDir(), "arguments.json")
	t.Setenv("FICTIONAL_ARGUMENTS_FILE", argumentsFile)
	files := map[string]string{
		"go.mod": "module example.com/fictional\n\ngo 1.26\n",
		"main.go": `package main
import (
 "encoding/json"
 "os"
)
func main() {
 data, err := json.Marshal(os.Args[1:])
 if err != nil { panic(err) }
 if err := os.WriteFile(os.Getenv("FICTIONAL_ARGUMENTS_FILE"), data, 0600); err != nil { panic(err) }
}
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(pipelineDirectory, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	previousDaemon := ensureRunDaemonFn
	ensureRunDaemonFn = func() {}
	t.Cleanup(func() { ensureRunDaemonFn = previousDaemon })
	want := []string{"fictional", "--sw-profile", "fictional", "--sw-mystery=value", "--", "literal"}
	arguments := append([]string{"fictional", "--sw-no-update", "--sw-cd", repository, "--"}, want[1:]...)
	if err := dispatchRun(arguments); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argumentsFile)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("executed arguments = %q, want %q", got, want)
	}
}

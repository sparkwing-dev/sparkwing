package nodehost

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
)

func TestSDKGateIndexReachesCheckingTools(t *testing.T) {
	if os.Getenv("W6_GATE_CONSUMER") == "1" {
		command := exec.Command("git", "ls-files")
		command.Env = os.Environ()
		if index := gitenv.GateIndex(); index != "" {
			command.Env = append(command.Env, "GIT_INDEX_FILE="+index)
		}
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	git := func(index string, args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", dir}, args...)...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
		if index != "" {
			command.Env = append(command.Env, "GIT_INDEX_FILE="+index)
		}
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("", "init")
	for _, name := range []string{"default", "alternate"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("", "add", "default")
	index := filepath.Join(t.TempDir(), "commit-index")
	git(index, "add", "alternate")
	snapshots, err := NewFileEnvironmentSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seal, err := snapshots.Capture(t.Context(), []string{"PATH=" + os.Getenv("PATH"), "W6_GATE_CONSUMER=1", gitenv.GateIndexVar + "=" + index})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(seal)
	if err != nil || strings.Contains(string(encoded), index) {
		t.Fatalf("machine index path entered seal: %s, %v", encoded, err)
	}
	policy := SDKEnvironment{Snapshots: snapshots, Base: procenv.Base{Local: true}}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []procenv.Kind{procenv.Plan, procenv.Attempt, procenv.Eval} {
		environment, err := policy.build(t.Context(), kind, seal, procenv.Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(executable, "-test.run=^TestSDKGateIndexReachesCheckingTools$")
		command.Env, command.Dir = environment, dir
		out, err := command.CombinedOutput()
		if err != nil || string(out) != "alternate\n" {
			t.Errorf("kind %d checked wrong index: %q, %v", kind, out, err)
		}
	}
	remote := SDKEnvironment{Base: procenv.Base{Ambient: []string{gitenv.GateIndexVar + "=" + index}, SubmitEnvAllow: []string{gitenv.GateIndexVar}}}
	environment, err := remote.build(t.Context(), procenv.Plan, seal, procenv.Runtime{GateIndex: index})
	if err != nil || strings.Contains(strings.Join(environment, "\n"), index) {
		t.Fatalf("remote inherited machine binding: %v, %v", environment, err)
	}
}

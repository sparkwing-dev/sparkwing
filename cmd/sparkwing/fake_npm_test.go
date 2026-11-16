package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

type fakeNPMConfig struct {
	Calls    string `json:"calls"`
	ExitCode int    `json:"exit_code"`
}

// hack: the test binary doubles as npm so scaffold tests stay off the network on every OS;
// as npm.exe it skips Windows' node npm-cli.js route. Settings sit beside it: install environments filter variables.
func fakeNPM(t *testing.T, exitCode int) string {
	t.Helper()
	bin, calls := installFakeNPM(t, runtime.GOOS, exitCode)
	t.Setenv("PATH", bin)
	return calls
}

func installFakeNPM(t *testing.T, goos string, exitCode int) (string, string) {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	name := "npm"
	if goos == "windows" {
		name += ".exe"
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := linkOrCopyExecutable(self, filepath.Join(bin, name)); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(fakeNPMConfig{Calls: calls, ExitCode: exitCode})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "fake-npm.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func TestFakeNPMRunsThroughTheWindowsDispatcher(t *testing.T) {
	bin, calls := installFakeNPM(t, "windows", 0)
	lookPath := func(name string) (string, error) {
		if name != "npm" {
			return "", fmt.Errorf("%s is not installed", name)
		}
		return filepath.Join(bin, "npm.exe"), nil
	}
	cmd, err := pipelineentry.NPMCommand(t.Context(), procenv.Env{}, "windows", lookPath, engine.TypeScriptLockfileArgs()...)
	if err != nil {
		t.Fatalf("the Windows npm route cannot run the fixture: %v", err)
	}
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture as npm.exe: %v: %s", err, output)
	}
	recorded, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(recorded), "|install --package-lock-only") {
		t.Fatalf("fixture did not record the npm call: %q %v", recorded, err)
	}
	if _, err := os.Stat(filepath.Join(cmd.Dir, "package-lock.json")); err != nil {
		t.Fatalf("fixture did not write the lockfile: %v", err)
	}
}

func linkOrCopyExecutable(source, target string) error {
	if os.Link(source, target) == nil {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		return errors.Join(err, out.Close())
	}
	return out.Close()
}

func init() {
	if name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"); name == "npm" {
		os.Exit(runFakeNPM())
	}
}

func runFakeNPM() int {
	self, err := os.Executable()
	if err != nil {
		return 70
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(self), "fake-npm.json"))
	if err != nil {
		return 70
	}
	var config fakeNPMConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return 70
	}
	directory, err := os.Getwd()
	if err == nil {
		directory, err = filepath.EvalSymlinks(directory)
	}
	if err != nil {
		return 70
	}
	record, err := os.OpenFile(config.Calls, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 70
	}
	_, writeErr := fmt.Fprintf(record, "%s|%s\n", directory, strings.Join(os.Args[1:], " "))
	if err := errors.Join(writeErr, record.Close()); err != nil {
		return 70
	}
	if config.ExitCode != 0 {
		fmt.Fprintln(os.Stderr, "npm error network request failed")
		return config.ExitCode
	}
	lock := `{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"` + typeScriptSDKVersion + `"}},"node_modules/@sparkwing/sdk":{"version":"` + typeScriptSDKVersion + `"}}}` + "\n"
	if err := os.WriteFile("package-lock.json", []byte(lock), 0o600); err != nil {
		return 70
	}
	return 0
}

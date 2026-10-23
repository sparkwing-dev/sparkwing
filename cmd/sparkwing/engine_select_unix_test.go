//go:build !windows

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
)

func TestPipelineEngineExecHelper(t *testing.T) {
	separator := slices.Index(os.Args, "--engine-fixture")
	if separator < 0 {
		return
	}
	selected := os.Args[separator+1]
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v1.2.3"}
	command := pipelineEngineSwitcher{
		current: current, diagnostics: io.Discard,
		target:     releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		readPin:    func(string, engineid.SDKKind) (engineid.Pin, error) { return pin, nil },
		versionFor: func(engineid.Pin) (string, error) { return "v1.2.3", nil },
		execute:    execToolchain,
		resolve: func(_ context.Context, _ engineid.Pin, target releaseasset.Target, _ toolchain.Mode) (engine.Resolved, error) {
			return engine.Resolved{Identity: engineid.Identity{Pin: pin, Version: "v1.2.3", ManifestDigest: "fixture-manifest"}, Target: target, AssetDigest: "fixture-asset", Executable: selected}, nil
		},
	}
	if err := command.run(t.Context(), "/checkout/.sparkwing", engineid.GoSDK, "auto", os.Args[separator+2:], os.Environ()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("selected process returned")
}

func TestPipelineEngineReplaysAfterRootFlagsInRealProcess(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "engine-fixture")
	script := "#!/bin/sh\nprintf 'cwd:%s\\n' \"$PWD\"\nprintf 'captured:%s\\n' \"$CAPTURED_VALUE\"\nprintf 'arg:%s\\n' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "release", "--", "--name=with spaces", "literal;$()"}
	cmd := exec.CommandContext(t.Context(), self, append([]string{"-test.run=^TestPipelineEngineExecHelper$", "--", "--engine-fixture", bin}, args...)...)
	cmd.Dir, cmd.Env = dir, []string{"PATH=/usr/bin:/bin", "CAPTURED_VALUE=exact snapshot"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("engine fixture: %v\n%s", err, out)
	}
	want := "cwd:" + dir + "\ncaptured:exact snapshot\narg:" + strings.Join(args, "\narg:") + "\n"
	if string(out) != want {
		t.Fatalf("replayed invocation changed: %q", out)
	}
}

func TestPipelineEngineSameFileAvoidsRecursiveExecution(t *testing.T) {
	command, selected, _, calls := engineSwitchFixture(t, engineid.GoSDK)
	alias := filepath.Join(t.TempDir(), "linked-engine")
	if err := os.Link(selected.Executable, alias); err != nil {
		t.Fatal(err)
	}
	command.current = alias
	if err := command.run(t.Context(), "/checkout/.sparkwing", engineid.GoSDK, "auto", nil, nil); err != nil || *calls != 0 {
		t.Fatalf("same verified file re-executed: %v calls=%d", err, *calls)
	}
}

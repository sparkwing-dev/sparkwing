package main

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

// perf: the helpers a suite re-execs inherit its CLI build, so one package run builds it once.
const testEngineVariable = "SPARKWING_TEST_ENGINE_BINARY"

func useInheritedTestEngine() bool {
	binary := os.Getenv(testEngineVariable)
	if binary != "" {
		engine.UseInstalledExecutableForTest(binary)
	}
	return binary != ""
}

// safety: Go records a test binary's main package as cmd/sparkwing.test, and the retained-engine check accepts only
// cmd/sparkwing builds, so replacement pins resolve to a real build of this package instead of the test binary.
func useBuiltTestEngine() (func(), error) {
	dir, err := os.MkdirTemp("", "sparkwing-test-engine-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	binary := filepath.Join(dir, "sparkwing")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if err := buildTestEngine(binary); err != nil {
		cleanup()
		return nil, err
	}
	if err := os.Setenv(testEngineVariable, binary); err != nil {
		cleanup()
		return nil, err
	}
	engine.UseInstalledExecutableForTest(binary)
	return cleanup, nil
}

func buildTestEngine(binary string) error {
	suite, ok := debug.ReadBuildInfo()
	if !ok {
		return errors.New("test binary carries no build information")
	}
	args := []string{"build", "-o", binary}
	// safety: code re-executed through the engine keeps the suite's build tags and race detection.
	for _, setting := range suite.Settings {
		switch {
		case setting.Key == "-race" && setting.Value == "true":
			args = append(args, "-race")
		case setting.Key == "-tags" && setting.Value != "":
			args = append(args, "-tags="+setting.Value)
		}
	}
	command := exec.Command("go", append(args, ".")...)
	// safety: GOTOOLCHAIN=local stops the go command from switching toolchains, and the version check refuses a
	// build from any toolchain but the suite's.
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("build the CLI test engine: %w\n%s", err, output)
	}
	built, err := buildinfo.ReadFile(binary)
	if err != nil {
		return err
	}
	if built.GoVersion != suite.GoVersion {
		return fmt.Errorf("CLI test engine built with %s, but the suite runs %s", built.GoVersion, suite.GoVersion)
	}
	return nil
}

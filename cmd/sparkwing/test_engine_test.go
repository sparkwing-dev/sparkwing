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
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

// perf: the helpers a suite re-execs inherit its CLI build, so one package run builds it once.
const testEngineVariable = "SPARKWING_TEST_ENGINE_BINARY"

var testEngine func() (string, error)

// safety: Go records a test binary's main package as cmd/sparkwing.test, and the retained-engine check accepts only
// cmd/sparkwing builds, so replacement pins resolve to a real build of this package instead of the test binary. The
// build waits for the first replacement pin, so a helper started with a bare environment never needs a go command,
// and it uses the go command, environment, package directory and temporary root seen before any test replaced them.
func useTestEngine() func() {
	if binary := os.Getenv(testEngineVariable); binary != "" {
		testEngine = func() (string, error) { return binary, nil }
		engine.UseInstalledExecutableForTest(testEngine)
		return func() {}
	}
	environment, root := os.Environ(), os.TempDir()
	packageDir, dirErr := os.Getwd()
	goCommand, lookErr := exec.LookPath("go")
	var (
		once     sync.Once
		dir      string
		binary   string
		buildErr error
	)
	testEngine = func() (string, error) {
		once.Do(func() {
			if err := errors.Join(lookErr, dirErr); err != nil {
				buildErr = fmt.Errorf("build the CLI test engine: %w", err)
				return
			}
			if dir, buildErr = os.MkdirTemp(root, "sparkwing-test-engine-"); buildErr != nil {
				return
			}
			binary = filepath.Join(dir, "sparkwing")
			if runtime.GOOS == "windows" {
				binary += ".exe"
			}
			if buildErr = buildTestEngine(goCommand, environment, packageDir, binary); buildErr == nil {
				buildErr = os.Setenv(testEngineVariable, binary)
			}
		})
		return binary, buildErr
	}
	engine.UseInstalledExecutableForTest(testEngine)
	return func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
}

func buildTestEngine(goCommand string, environment []string, packageDir, binary string) error {
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
	command := exec.Command(goCommand, append(args, ".")...)
	// safety: GOTOOLCHAIN=local stops the go command from switching toolchains, and the version check refuses a
	// build from any toolchain but the suite's.
	command.Dir, command.Env = packageDir, append(environment, "GOTOOLCHAIN=local")
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

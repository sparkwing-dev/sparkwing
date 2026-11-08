package services

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	// hack: stubDocker symlinks this binary onto PATH as docker, so an
	// invocation under that name is a stub run rather than the suite.
	if filepath.Base(os.Args[0]) == "docker" {
		os.Exit(runStubDocker(os.Args[1:]))
	}
	if runtime.GOOS == "windows" {
		exe, _ := os.Executable()
		if filepath.Base(exe) == "docker.exe" {
			if os.Getenv(stubDockerArgvEnv) == "" {
				os.Exit(2)
			}
			os.Exit(runStubDocker(os.Args[1:]))
		}
	}
	testleak.Main(m)
}

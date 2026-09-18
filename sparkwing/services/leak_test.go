package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	// hack: stubDocker symlinks this binary onto PATH as docker, so an
	// invocation under that name is a stub run rather than the suite.
	if filepath.Base(os.Args[0]) == "docker" {
		os.Exit(runStubDocker(os.Args[1:]))
	}
	testleak.Main(m)
}

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testmanager"
)

func TestMain(m *testing.M) {
	// safety: a copy of this test binary named sparkwing-controller-proof is the
	// fake controller the isolation-proof lifecycle test starts through the script.
	if filepath.Base(os.Args[0]) == fakeControllerName {
		os.Exit(runFakeController(os.Args[1:]))
	}
	testmanager.Main(m)
}

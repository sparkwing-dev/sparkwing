package orchestrator_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	status := m.Run()
	if err := cleanupE2EFixture(); err != nil {
		fmt.Fprintf(os.Stderr, "remove e2e fixture: %v\n", err)
		status = 1
	}
	if err := testleak.Check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		status = 1
	}
	os.Exit(status)
}

var e2eFixtureRoot string

func cleanupE2EFixture() error {
	if e2eFixtureRoot == "" {
		return nil
	}
	return os.RemoveAll(e2eFixtureRoot)
}

package orchestrator_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	status := m.Run()
	if err := cleanupProcessPerNodeFixture(); err != nil {
		fmt.Fprintf(os.Stderr, "remove process-per-node fixture: %v\n", err)
		status = 1
	}
	if err := testleak.Check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		status = 1
	}
	os.Exit(status)
}

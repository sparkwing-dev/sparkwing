package storetest

import (
	"fmt"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testleak"
)

// Main closes PostgreSQL templates owned by the process and checks for leaked goroutines.
// Test packages using these fixtures call it from TestMain.
func Main(m *testing.M) {
	code := m.Run()
	if err := closePostgresTemplates(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	if err := testleak.Check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code) //nolint:forbidigo // TestMain owns the process exit after fixture and sandbox cleanup.
}

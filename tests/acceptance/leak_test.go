package acceptance_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testmanager"

	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testleak"
)

func TestMain(m *testing.M) {
	os.Exit(testmanager.Run(func() int {
		code := m.Run()
		if err := testleak.Check(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
		return code
	}))
}

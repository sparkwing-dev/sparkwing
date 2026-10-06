//go:build !windows

package localsecrets_test

import (
	"os"
	"testing"
)

func makeLegacyDirectoryUnreadable(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
}

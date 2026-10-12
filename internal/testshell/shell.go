package testshell

import (
	"os"
	"path/filepath"
	"testing"
)

// Install makes a shell fixture callable through the platform's executable lookup.
func Install(t *testing.T, name, script string) string {
	t.Helper()
	if bin, ok := nativeInstall(t, name, script); ok {
		return bin
	}
	if err := os.WriteFile(name, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(name)
}

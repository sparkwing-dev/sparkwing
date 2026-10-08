package toolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveHoldReportsUnsafeAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	large := filepath.Join(root, "large")
	if err := os.WriteFile(large, []byte(strings.Repeat("v", maxHoldBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, large} {
		hold, err := ResolveHold(path)
		if err == nil || hold.Source != path {
			t.Fatalf("unsafe hold disappeared: %+v %v", hold, err)
		}
		if _, err := ResolveHoldStrict(path); err == nil {
			t.Fatal("strict resolver hid read failure")
		}
	}
	missing := filepath.Join(root, "missing")
	if hold, err := ResolveHold(missing); err != nil || hold.Value != "" {
		t.Fatalf("missing hold was not absent: %+v %v", hold, err)
	}
}

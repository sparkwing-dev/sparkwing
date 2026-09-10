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
		hold, err := ResolveHold(Hold{}, path)
		if err == nil || hold.Source != path {
			t.Fatalf("unsafe hold disappeared: %+v %v", hold, err)
		}
		if _, err := ResolveHoldStrict(Hold{}, path); err == nil {
			t.Fatal("strict resolver hid read failure")
		}
		hold, err = ResolveHold(Hold{Value: "v0.48", Source: "environment"}, path)
		if err != nil || hold.Value != "v0.48" {
			t.Fatalf("environment did not retain precedence: %+v %v", hold, err)
		}
	}
	missing := filepath.Join(root, "missing")
	if hold, err := ResolveHold(Hold{}, missing); err != nil || hold.Value != "" {
		t.Fatalf("missing hold was not absent: %+v %v", hold, err)
	}
}

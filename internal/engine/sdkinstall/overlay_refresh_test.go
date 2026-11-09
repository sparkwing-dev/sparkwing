package sdkinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshOverlayPinRefusesSymlinkedOverlay(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, ".resolved.mod")
	if err := os.Symlink(outside, overlay); err != nil {
		t.Fatal(err)
	}
	if err := refreshOverlayPin(overlay, "v0.68.4"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlinked overlay accepted: %v", err)
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "keep\n" {
		t.Fatalf("symlink target changed: %q, %v", body, err)
	}
}

func TestRefreshOverlayPinUpdatesRegularOverlay(t *testing.T) {
	overlay := filepath.Join(t.TempDir(), ".resolved.mod")
	if err := os.WriteFile(overlay, []byte("module example.test/pipelines\n\ngo 1.26\n\nrequire github.com/sparkwing-dev/sparkwing/sparkwing v0.68.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := refreshOverlayPin(overlay, "v0.68.4"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(overlay)
	if err != nil || !strings.Contains(string(body), "github.com/sparkwing-dev/sparkwing/sparkwing v0.68.4") {
		t.Fatalf("overlay not refreshed: %s, %v", body, err)
	}
}

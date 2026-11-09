//go:build !windows

package artifacts

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCaptureRejectsFIFO(t *testing.T) {
	ws := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(ws, "out"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, _, _ := testData()
	if _, err := Capture(t.Context(), d, ws, []string{"out"}, func(string) {}); err == nil || !strings.Contains(err.Error(), "exclude it from the artifact glob or write a regular file") {
		t.Fatalf("Capture FIFO: %v", err)
	}
}

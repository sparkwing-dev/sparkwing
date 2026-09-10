//go:build !windows

package toolchain

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestResolveHoldRejectsFIFO(t *testing.T) {
	if path := os.Getenv("SPARKWING_HOLD_FIFO_FIXTURE"); path != "" {
		if _, err := ResolveHold(Hold{}, path); err == nil {
			t.Fatal("FIFO hold appeared absent")
		}
		return
	}
	path := filepath.Join(t.TempDir(), "hold")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestResolveHoldRejectsFIFO$")
	cmd.Env = append(os.Environ(), "SPARKWING_HOLD_FIFO_FIXTURE="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO hold read blocked: %v %s", err, out)
	}
}

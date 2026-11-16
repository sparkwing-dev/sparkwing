package runners

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetPausedIsIdempotentAndPrivate(t *testing.T) {
	dir := t.TempDir()
	for _, step := range []struct {
		paused, changed bool
	}{{true, true}, {true, false}, {false, true}, {false, false}} {
		changed, err := SetPaused(dir, step.paused)
		if err != nil || changed != step.changed {
			t.Fatalf("SetPaused(%v) = %v, %v; want changed=%v", step.paused, changed, err, step.changed)
		}
		paused, err := Paused(dir)
		if err != nil || paused != step.paused {
			t.Fatalf("Paused = %v, %v; want %v", paused, err, step.paused)
		}
		if step.paused {
			info, err := os.Stat(filepath.Join(dir, "paused"))
			if err != nil || info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("pause marker is not owner-only: %v %v", info, err)
			}
		}
	}
	if _, err := SetPaused(" ", true); err == nil {
		t.Fatal("a runner without credentials_dir was paused")
	}
}

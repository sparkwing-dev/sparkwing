package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDeployCleanupKeepsSymlinkSwapsInsideRoot(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	var protected []string
	for i := range 32 {
		path := filepath.Join(outside, fmt.Sprint(i))
		if err := os.Mkdir(path, 0o500); err != nil {
			t.Fatal(err)
		}
		protected = append(protected, path)
	}
	protected = append(protected, outside)
	if err := os.Chmod(outside, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, path := range protected {
			_ = os.Chmod(path, 0o700)
		}
	})
	var swaps atomic.Int64
	for range 64 {
		root, err := os.MkdirTemp(base, "cleanup-")
		if err != nil {
			t.Fatal(err)
		}
		slot, held := filepath.Join(root, "slot"), filepath.Join(base, "swap")
		if err := os.Mkdir(slot, 0o700); err != nil {
			t.Fatal(err)
		}
		for i := range 32 {
			if err := os.Mkdir(filepath.Join(slot, fmt.Sprint(i)), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(outside, held); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := unix.Renameat2(unix.AT_FDCWD, slot, unix.AT_FDCWD, held, unix.RENAME_EXCHANGE); err != nil {
				t.Fatal(err)
			}
		}
		started, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			close(started)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if unix.Renameat2(unix.AT_FDCWD, slot, unix.AT_FDCWD, held, unix.RENAME_EXCHANGE) == nil {
					swaps.Add(1)
				}
			}
		}()
		<-started
		_ = removeDeployDir(root)
		close(stop)
		<-done
		for _, path := range protected {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o500 {
				t.Fatalf("cleanup changed permissions outside its root: %s = %o", path, info.Mode().Perm())
			}
		}
		if err := os.RemoveAll(held); err != nil {
			t.Fatal(err)
		}
	}
	if swaps.Load() == 0 {
		t.Fatal("did not exercise a symlink swap during cleanup")
	}
}

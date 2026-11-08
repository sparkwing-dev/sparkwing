//go:build unix

package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStableProductTestHomeExcludesAnotherOwnerAndCleansUp(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	gateFixtureRepo(t)
	var previous string
	failure := errors.New("suite failed")
	for range 2 {
		err := withStableProductTestHome("overlap", func(home string) error {
			if previous != "" && home != previous {
				t.Fatalf("home changed from %q to %q", previous, home)
			}
			previous = home
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("suite started with leftover state: %v, %v", entries, err)
			}
			other, err := os.OpenFile(home+".lock", os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
				t.Fatalf("overlapping owner acquired the active suite home: %v", err)
			}
			if err := os.WriteFile(filepath.Join(home, "suite-state"), []byte("private"), 0o600); err != nil {
				t.Fatal(err)
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("suite error = %v", err)
		}
		if _, err := os.Stat(previous); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("suite home survived cleanup: %v", err)
		}
		lock, err := os.OpenFile(previous+".lock", os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Errorf("finished suite retained its lock: %v", err)
		}
		_ = lock.Close()
	}
}

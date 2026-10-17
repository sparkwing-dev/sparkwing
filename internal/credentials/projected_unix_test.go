//go:build !windows

package credentials_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
)

func TestCredentialTargetPermissionsAndConfinement(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "credentials")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(directory, "target")
	outside := filepath.Join(root, "outside")
	for _, path := range []string{inside, outside} {
		if err := os.WriteFile(path, []byte("credential"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := credentials.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "token")
	property := func(bits uint16, escape, absolute bool) bool {
		target := inside
		if escape {
			target = outside
		}
		if err := os.Chmod(target, 0o600|os.FileMode(bits&0o77)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		destination := target
		if !absolute {
			var err error
			destination, err = filepath.Rel(directory, target)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(destination, link); err != nil {
			t.Fatal(err)
		}
		got, err := dir.Read("token")
		if escape || bits&0o22 != 0 {
			return err != nil && got == ""
		}
		return err == nil && got == "credential"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}

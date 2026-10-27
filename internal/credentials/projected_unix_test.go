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
		if escape || bits&0o02 != 0 {
			return err != nil && got == ""
		}
		return err == nil && got == "credential"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}

// A pod volume under fsGroup: kubelet leaves the root setgid 0770 and the epoch
// group-writable under the pod's own group.
func TestEpochLoadsFromAnFSGroupPodVolume(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chown(dir, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, os.ModeSetgid|0o770); err != nil {
		t.Fatal(err)
	}
	first, err := credentials.LoadOrCreateEpoch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "credential-epoch"), 0o660); err != nil {
		t.Fatal(err)
	}
	again, err := credentials.LoadOrCreateEpoch(dir)
	if err != nil || again != first {
		t.Fatalf("restart on a pod volume = %q %v, want %q", again, err, first)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode()&(os.ModeSetgid|os.ModePerm) != os.ModeSetgid|0o770 {
		t.Fatalf("epoch directory = %v %v, want setgid 0770 left for kubelet", info, err)
	}
}

//go:build !windows

package fssecure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerOnlyAdmitsGroupReadOnlyForOwnGroups(t *testing.T) {
	own := []int{100, 65534}
	for _, tc := range []struct {
		perm os.FileMode
		gid  uint32
		ok   bool
	}{
		{0o400, 1, true},
		{0o600, 1, true},
		{0o440, 65534, true},
		{0o440, 100, true},
		{0o440, 4242, false},
		{0o444, 65534, false},
		{0o460, 65534, false},
		{0o450, 65534, false},
		{0o404, 65534, false},
	} {
		if err := ownerOnly(tc.perm, tc.gid, own); (err == nil) != tc.ok {
			t.Errorf("ownerOnly(%04o, gid %d) = %v, want ok=%v", tc.perm, tc.gid, err, tc.ok)
		}
	}
}

func TestOpenPrivateCredentialAcceptsGroupReadUnderTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oidc-key")
	if err := os.WriteFile(path, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	for mode, ok := range map[os.FileMode]bool{0o440: true, 0o444: false, 0o460: false} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		f, err := OpenPrivateCredential(dir, "oidc-key")
		if err == nil {
			_ = f.Close()
		}
		if (err == nil) != ok {
			t.Errorf("mode %04o: %v, want ok=%v", mode, err, ok)
		}
	}
}

func TestCredentialGroupWriteOnlyForOwnGroups(t *testing.T) {
	own := []int{100, 65534}
	for _, tc := range []struct {
		perm os.FileMode
		gid  uint32
		ok   bool
	}{
		{0o600, 1, true},
		{0o660, 65534, true},
		{0o660, 4242, false},
		{0o662, 65534, false},
		{0o602, 65534, false},
	} {
		if err := groupWritableOnlyByOwnGroup(tc.perm, tc.gid, own); (err == nil) != tc.ok {
			t.Errorf("groupWritableOnlyByOwnGroup(%04o, gid %d) = %v, want ok=%v", tc.perm, tc.gid, err, tc.ok)
		}
	}
}

// Kubelet's fsGroupChangePolicy OnRootMismatch skips relabelling only when the
// volume root keeps setgid group rwx; taking it away made every controller
// restart re-add group write to the credential epoch.
func TestEnsureDirKeepsTheFSGroupModeOfAPodVolume(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chown(dir, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, os.ModeSetgid|0o777); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode() & (os.ModeSetgid | os.ModePerm); got != os.ModeSetgid|0o770 {
		t.Fatalf("pod volume mode = %v, want setgid 0770", got)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("plain directory = %v %v, want 0700", info, err)
	}
}

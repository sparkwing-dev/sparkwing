//go:build !windows

package fssecure

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
	for mode, ok := range map[os.FileMode]bool{0o440: true, 0o444: false, 0o460: false, 0o660: false} {
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
// volume root keeps setgid group rwx; without it each controller restart
// re-adds group write to the credential epoch.
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

// Only state the controller writes on a kubelet-managed volume may be
// group-writable, and only under the process's own group.
func TestOnlyStateCredentialsAdmitOwnGroupWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	for mode, state := range map[os.FileMode]bool{0o660: true, 0o662: false} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		for open, want := range map[string]bool{"credential": false, "state": state} {
			opener := OpenCredential
			if open == "state" {
				opener = OpenStateCredential
			}
			f, err := opener(dir, "target")
			if err == nil {
				_ = f.Close()
			}
			if (err == nil) != want {
				t.Errorf("%s at %04o: %v, want ok=%v", open, mode, err, want)
			}
		}
	}
}

func TestOpenCredentialDistinguishesMissingFromDanglingLink(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenCredential(dir, "token"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing credential: %v, want not-exist", err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, "token")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCredential(dir, "token"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling credential link: %v, want refusal distinct from missing", err)
	}
}

func TestOpenCredentialConcurrentPublication(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := range 500 {
		name := fmt.Sprintf("key-%d", attempt)
		ready := make(chan struct{}, 8)
		var readers sync.WaitGroup
		for range 8 {
			readers.Go(func() {
				if _, err := OpenCredential(dir, name); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("before publication: %v, want missing", err)
				}
				ready <- struct{}{}
				for {
					file, err := OpenCredential(dir, name)
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					if err != nil {
						t.Errorf("concurrent publication: %v", err)
					} else if err := file.Close(); err != nil {
						t.Error(err)
					}
					return
				}
			})
		}
		for range 8 {
			<-ready
		}
		if err := os.Link(source, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		readers.Wait()
		if t.Failed() {
			return
		}
	}
}

//go:build !windows

package wingd

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shortSocketBase(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "swsock")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	return base
}

func TestEnsureSocketDir_RefusesAnUnsafeDirectory(t *testing.T) {
	base := shortSocketBase(t)
	cases := []struct {
		name    string
		prepare func(t *testing.T, path string)
		want    string
	}{
		{
			name:    "fresh",
			prepare: func(*testing.T, string) {},
		},
		{
			name: "already private",
			prepare: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "group and world writable",
			prepare: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o777); err != nil {
					t.Fatal(err)
				}
			},
			want: "mode 0777, want 0700",
		},
		{
			name: "symlink",
			prepare: func(t *testing.T, path string) {
				target := path + "-target"
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
			want: "not a directory",
		},
		{
			name: "regular file",
			prepare: func(t *testing.T, path string) {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "not a directory",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(base, strings.ReplaceAll(tc.name, " ", "-"))
			tc.prepare(t, path)
			err := ensureSocketDir(path)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ensureSocketDir(%s) = %v, want nil", path, err)
				}
				info, serr := os.Lstat(path)
				if serr != nil {
					t.Fatal(serr)
				}
				if perm := info.Mode().Perm(); perm != 0o700 {
					t.Fatalf("socket directory mode %#o, want 0700", perm)
				}
				return
			}
			if err == nil {
				t.Fatalf("ensureSocketDir(%s) accepted an unsafe directory", path)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name the fault %q", err, tc.want)
			}
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf("error %v does not report a permission fault", err)
			}
		})
	}
}

func TestSocketDirFault_RejectsAnotherUsersDirectory(t *testing.T) {
	dir := filepath.Join(shortSocketBase(t), "foreign")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fault := socketDirFault(info); fault != "" {
		t.Fatalf("this user's own 0700 directory was rejected: %s", fault)
	}
	if socketDirFault(foreignOwner{FileInfo: info}) == "" {
		t.Fatal("a directory owned by another uid was accepted")
	}
}

func TestValidateSocketDir_AllowsAnAbsentDirectory(t *testing.T) {
	sock := filepath.Join(shortSocketBase(t), "absent", "d.sock")
	if err := ValidateSocketDir(sock); err != nil {
		t.Fatalf("ValidateSocketDir on an unused path = %v, want nil", err)
	}
	dir := filepath.Dir(sock)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSocketDir(sock); err == nil {
		t.Fatal("ValidateSocketDir accepted a world-readable socket directory")
	}
}

func TestSocketBaseDir_IgnoresTheEnvironment(t *testing.T) {
	base := testSocketBase(t)
	t.Setenv("XDG_RUNTIME_DIR", shortSocketBase(t))
	if got := testSocketBase(t); got != base {
		t.Fatalf("socketBaseDir() = %q with a runtime directory set, want the fixed base %q", got, base)
	}
	t.Setenv("TMPDIR", shortSocketBase(t))
	if got := testSocketBase(t); got != base {
		t.Fatalf("socketBaseDir() = %q with TMPDIR set, want the fixed base %q", got, base)
	}
	home := t.TempDir()
	sock, err := EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	_, identity, err := socketHomeIdentity(home)
	if err != nil {
		t.Fatal(err)
	}
	if want := socketPathIn(base, identity, recordedSocketToken(t, home)); sock != want {
		t.Fatalf("SocketPath = %q, want the derived path %q", sock, want)
	}
}

func TestCheckSocketBase_RefusesAWritableBaseWithoutTheStickyBit(t *testing.T) {
	base := shortSocketBase(t)
	if err := checkSocketBase(base); err != nil {
		t.Fatalf("a private base was refused: %v", err)
	}
	if err := os.Chmod(base, 0o777); err != nil {
		t.Fatal(err)
	}
	err := checkSocketBase(base)
	if err == nil {
		t.Fatal("a world-writable base with no sticky bit was accepted")
	}
	if !strings.Contains(err.Error(), base) || !strings.Contains(err.Error(), "sticky") {
		t.Errorf("error %v does not name the base and the missing sticky bit", err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error %v does not report a permission fault", err)
	}
	if err := ensureSocketDir(filepath.Join(base, socketDirPrefix()+"abc")); err == nil {
		t.Fatal("a socket directory was created under an unsafe base")
	}
	if err := os.Chmod(base, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if err := checkSocketBase(base); err != nil {
		t.Fatalf("a sticky world-writable base was refused: %v", err)
	}
	if err := checkSocketBase(filepath.Join(base, "absent")); err == nil {
		t.Fatal("a missing base was accepted")
	}
}

func TestClients_RefuseAForeignDirectoryAtTheDerivedPath(t *testing.T) {
	home := t.TempDir()
	sock, err := EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o700)
		_ = os.RemoveAll(dir)
	})
	// safety: a directory this user cannot write stands in for one another
	// account planted, which only root can create in a test.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}

	again, err := EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if again != sock {
		t.Fatalf("SocketPath moved to %q; a planted directory must never redirect callers off %q", again, sock)
	}

	derr := ValidateSocketDir(sock)
	if derr == nil {
		t.Fatal("a client would dial into a foreign socket directory")
	}
	if !strings.Contains(derr.Error(), dir) {
		t.Errorf("dial refusal %v does not name the directory %q", derr, dir)
	}
}

func TestPrepareDaemonSocket_ClearsAStaleSocket(t *testing.T) {
	home := t.TempDir()
	sock, err := EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(sock)) })
	_ = listenAt(t, sock).Close()

	state, err := PrepareDaemonSocket(home)
	if err != nil {
		t.Fatal(err)
	}
	if state != SocketPreparationReady {
		t.Fatalf("preparation = %v, want ready", state)
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatalf("stale socket survived preparation: %v", err)
	}
}

func TestDaemon_BindsAPrivateSocket(t *testing.T) {
	home := t.TempDir()
	d, err := New(Config{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-errc:
		t.Fatalf("daemon exited before serving: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("daemon never became ready")
	}

	info, err := os.Lstat(d.layout.sock)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode %#o, want 0600", perm)
	}
	dirInfo, err := os.Lstat(filepath.Dir(d.layout.sock))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket directory mode %#o, want 0700", perm)
	}

	cancel()
	select {
	case <-errc:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop after cancel")
	}
}

func TestDaemon_NeverBindsInAnExposedDirectory(t *testing.T) {
	home := t.TempDir()
	d, err := New(Config{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(d.layout.sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}

	ln, err := d.bindListener()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.RemoveAll(filepath.Dir(d.layout.sock))
	})
	if filepath.Dir(ln.Addr().String()) == dir || ValidateSocketDir(ln.Addr().String()) != nil {
		t.Fatalf("daemon served %s out of the world-writable directory %s", ln.Addr(), dir)
	}
}

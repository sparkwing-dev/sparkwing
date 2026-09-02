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

func TestSocketBaseDir_PrefersAPrivateRuntimeDirectory(t *testing.T) {
	runtimeDir := shortSocketBase(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	if got := socketBaseDir(); got != runtimeDir {
		t.Fatalf("socketBaseDir() = %q, want the runtime directory %q", got, runtimeDir)
	}
	if err := os.Chmod(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := socketBaseDir(); got != tempSocketBaseDir() {
		t.Fatalf("socketBaseDir() with a loose runtime directory = %q, want %q", got, tempSocketBaseDir())
	}
	t.Setenv("XDG_RUNTIME_DIR", "relative/path")
	if got := socketBaseDir(); got != tempSocketBaseDir() {
		t.Fatalf("socketBaseDir() with a relative runtime directory = %q, want %q", got, tempSocketBaseDir())
	}
}

func TestSocketPath_FallsBackToADaemonAtThePreUpgradePath(t *testing.T) {
	home := t.TempDir()
	legacy := socketPathIn(tempSocketBaseDir(), home)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(legacy)) })
	ln := listenAt(t, legacy)

	t.Setenv("XDG_RUNTIME_DIR", shortSocketBase(t))
	sock, err := SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if sock != legacy {
		t.Fatalf("SocketPath = %q, want the bound pre-upgrade socket %q", sock, legacy)
	}

	_ = ln.Close()
	_ = os.Remove(legacy)
	sock, err = SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if sock == legacy {
		t.Fatalf("SocketPath still points at the vacated pre-upgrade socket %q", sock)
	}
}

func TestPrepareDaemonSocket_ClearsAStalePreUpgradeSocket(t *testing.T) {
	home := t.TempDir()
	legacy := socketPathIn(tempSocketBaseDir(), home)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(legacy)) })
	_ = listenAt(t, legacy).Close()

	t.Setenv("XDG_RUNTIME_DIR", shortSocketBase(t))
	state, err := PrepareDaemonSocket(home)
	if err != nil {
		t.Fatal(err)
	}
	if state != SocketPreparationReady {
		t.Fatalf("preparation = %v, want ready", state)
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("stale pre-upgrade socket survived preparation: %v", err)
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

	info, err := os.Lstat(d.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode %#o, want 0600", perm)
	}
	dirInfo, err := os.Lstat(filepath.Dir(d.SocketPath()))
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

func TestDaemon_RefusesToBindInAnExposedDirectory(t *testing.T) {
	home := t.TempDir()
	d, err := New(Config{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(d.SocketPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = d.Run(ctx)
	if err == nil {
		t.Fatal("daemon served out of a world-writable socket directory")
	}
	if !strings.Contains(err.Error(), "socket directory") {
		t.Errorf("error %v does not name the socket directory", err)
	}
}

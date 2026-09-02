//go:build !windows

package fssecure_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestConfigDirHonorsXDGConfigHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got, err := fssecure.ConfigFile("secrets.env")
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}
	if want := filepath.Join(xdg, "sparkwing", "secrets.env"); got != want {
		t.Errorf("ConfigFile = %q, want %q", got, want)
	}
}

func TestEnsureConfigDirTightensInsideTheConfigRoot(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	if _, err := ensureConfigDirCapturingStderr(t, dir); err != nil {
		t.Fatalf("EnsureConfigDir: %v", err)
	}
	if got := dirMode(t, dir); got != 0o700 {
		t.Errorf("config directory mode = %#o, want 0700", got)
	}
}

func TestEnsureConfigDirKeepsASharedDirectoryAsTheOperatorLeftIt(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	shared := filepath.Join(t.TempDir(), "team-shared")
	if err := os.MkdirAll(shared, 0o775); err != nil {
		t.Fatalf("mkdir %s: %v", shared, err)
	}
	if err := os.Chmod(shared, 0o775); err != nil {
		t.Fatalf("chmod %s: %v", shared, err)
	}
	first, err := ensureConfigDirCapturingStderr(t, shared)
	if err != nil {
		t.Fatalf("EnsureConfigDir: %v", err)
	}
	if got := dirMode(t, shared); got != 0o775 {
		t.Errorf("shared directory mode = %#o, want 0775 left alone", got)
	}
	if !strings.Contains(first, shared) {
		t.Errorf("stderr = %q, want it to name %s", first, shared)
	}
	second, err := ensureConfigDirCapturingStderr(t, shared)
	if err != nil {
		t.Fatalf("EnsureConfigDir again: %v", err)
	}
	if second != "" {
		t.Errorf("second call wrote %q to stderr, want the warning once", second)
	}
}

func TestEnsureConfigDirCreatesAMissingSharedDirectoryPrivate(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(t.TempDir(), "elsewhere")
	if _, err := ensureConfigDirCapturingStderr(t, dir); err != nil {
		t.Fatalf("EnsureConfigDir: %v", err)
	}
	if got := dirMode(t, dir); got != 0o700 {
		t.Errorf("created directory mode = %#o, want 0700", got)
	}
}

func TestEnsureDirRefusesASymlinkedDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", target, err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", target, err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	err := fssecure.EnsureDir(link)
	if err == nil {
		t.Fatal("EnsureDir followed a symlinked directory")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error = %v, want it to name the symlink", err)
	}
	if got := dirMode(t, target); got != 0o755 {
		t.Errorf("symlink target mode = %#o, want 0755 untouched", got)
	}
}

func dirMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func ensureConfigDirCapturingStderr(t *testing.T, path string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	original := os.Stderr
	os.Stderr = w
	ensureErr := fssecure.EnsureConfigDir(path)
	os.Stderr = original
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	var buf strings.Builder
	if _, err := buf.Write(readAll(t, r)); err != nil {
		t.Fatalf("collect stderr: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	return buf.String(), ensureErr
}

func readAll(t *testing.T, f *os.File) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			return out
		}
	}
}

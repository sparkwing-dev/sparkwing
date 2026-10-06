//go:build windows

package fssecure

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileWindowsProducesReadablePrivateConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	for _, body := range []string{"first configuration", "replacement configuration"} {
		if err := WriteFile(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
		file, err := OpenPrivateConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || string(got) != body {
			t.Fatalf("private configuration roundtrip = %q, %v, %v", got, readErr, closeErr)
		}
	}
}

func TestTightenOpenWindowsRefusesDifferentFileIdentity(t *testing.T) {
	root := t.TempDir()
	originalPath, replacementPath := filepath.Join(root, "original"), filepath.Join(root, "replacement")
	original, err := os.Create(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if err := os.WriteFile(replacementPath, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = tightenPrivateOpen(original, func(string) (*os.File, error) {
		return os.Open(replacementPath)
	})
	if err == nil || !strings.Contains(err.Error(), "changed while it was opened") {
		t.Fatalf("different-file permission handle = %v", err)
	}
	info, err := os.Stat(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPrivateConfig(replacementPath, info); err == nil {
		t.Fatal("unrelated replacement file permissions were changed")
	}
}

func TestOpenFileWindowsPreservesAppendAndExclusiveFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	file, err := OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPrivateConfig(path, info); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("first"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("second"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("bad"), 0); err == nil {
		t.Fatal("append handle allowed WriteAt")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY); !os.IsExist(err) {
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("exclusive open of existing file = %v", err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "firstsecond" {
		t.Fatalf("append contents = %q, %v", body, err)
	}
}

func TestWriteFileWindowsSupportsLongNativePaths(t *testing.T) {
	dir := t.TempDir()
	for range 8 {
		dir = filepath.Join(dir, strings.Repeat("component", 4))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := WriteFile(path, []byte("long path configuration")); err != nil {
		t.Fatal(err)
	}
	file, err := OpenPrivateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
}

func TestPrivateConfigWindowsDetectsRealReplacementBeforeOpen(t *testing.T) {
	root := t.TempDir()
	path, replacement := filepath.Join(root, "config.yaml"), filepath.Join(root, "replacement.yaml")
	for _, candidate := range []string{path, replacement} {
		if err := WriteFile(candidate, []byte("private configuration")); err != nil {
			t.Fatal(err)
		}
	}
	file, err := openPrivateConfig(path, func(candidate string) (*os.File, error) {
		if err := os.Rename(replacement, candidate); err != nil {
			t.Fatal(err)
		}
		return openPrivateConfigFile(candidate)
	})
	if file != nil {
		_ = file.Close()
		t.Fatal("replacement returned a readable credential file")
	}
	if err == nil || !strings.Contains(err.Error(), "changed while it was opened") {
		t.Fatalf("real credential replacement error = %v", err)
	}
}

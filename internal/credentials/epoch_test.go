package credentials

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEpochSurvivesRestartAndRotates(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	first, err := LoadOrCreateEpoch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("epoch directory is not private: %v, %v", info, err)
		}
	}
	again, err := LoadOrCreateEpoch(dir)
	if err != nil || again != first {
		t.Fatalf("restart: %q %v", again, err)
	}
	next, err := RotateEpoch(dir)
	if err != nil || next == first {
		t.Fatalf("rotation: %q %v", next, err)
	}
	again, err = LoadOrCreateEpoch(dir)
	if err != nil || again != next {
		t.Fatalf("rotated restart: %q %v", again, err)
	}
	if err := os.WriteFile(epochPath(dir), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateEpoch(dir); err == nil {
		t.Fatal("accepted malformed epoch")
	}
}

func TestEpochRejectsUntrustedFiles(t *testing.T) {
	for _, kind := range []string{"group-writable", "escaping-symlink"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("Unix file modes and symlink creation")
			}
			dir := t.TempDir()
			path := epochPath(dir)
			if kind == "escaping-symlink" {
				target := filepath.Join(t.TempDir(), "epoch")
				if err := os.WriteFile(target, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o660); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadOrCreateEpoch(dir); err == nil {
				t.Fatalf("accepted %s credential epoch", kind)
			}
		})
	}
}

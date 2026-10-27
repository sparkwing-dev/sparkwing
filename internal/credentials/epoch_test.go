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
	if _, err := LoadEpoch(dir); !os.IsNotExist(err) {
		t.Fatalf("read-only epoch load created a fence: %v", err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o770 {
			t.Fatalf("read-only epoch load changed its directory: %v %v", info, err)
		}
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
	again, err := LoadEpoch(dir)
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
	for _, kind := range []string{"world-writable", "escaping-symlink"} {
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
				if err := os.Chmod(path, 0o662); err != nil {
					t.Fatal(err)
				}
			}
			for _, read := range []func(string) (string, error){LoadEpoch, LoadOrCreateEpoch} {
				if _, err := read(dir); err == nil {
					t.Fatalf("accepted %s credential epoch", kind)
				}
			}
		})
	}
}

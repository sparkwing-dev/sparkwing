package nodehost

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzPlanCacheSourceFingerprint(f *testing.F) {
	f.Add([]byte("source"), []byte("changed"))
	f.Add([]byte{}, []byte{0, '\n', 255})
	f.Fuzz(func(t *testing.T, before, after []byte) {
		root := t.TempDir()
		file := filepath.Join(root, "input")
		if err := os.WriteFile(file, before, 0o600); err != nil {
			t.Fatal(err)
		}
		first, err := fingerprintTree(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, append(append([]byte{}, before...), 0), 0o600); err != nil {
			t.Fatal(err)
		}
		second, err := fingerprintTree(t.Context(), root)
		if err != nil || first == second {
			t.Fatalf("changed source reused fingerprint: %v", err)
		}
		if err := os.Rename(file, filepath.Join(root, "renamed")); err != nil {
			t.Fatal(err)
		}
		renamed, err := fingerprintTree(t.Context(), root)
		if err != nil || renamed == second {
			t.Fatalf("renamed source reused fingerprint: %v", err)
		}
		if err := os.WriteFile(file, after, 0o600); err != nil {
			t.Fatal(err)
		}
		added, err := fingerprintTree(t.Context(), root)
		if err != nil || added == renamed {
			t.Fatalf("added dependency reused fingerprint: %v", err)
		}
	})
}

func TestPlanCacheSourceFingerprintRefusesUnknownDependencies(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "dependency")); err != nil {
		t.Fatal(err)
	}
	if _, err := fingerprintTree(t.Context(), root); err == nil {
		t.Fatal("external dependency tree received a cache fingerprint")
	}
}

func TestPlanCacheSourceFingerprintRefusesExternalGitDependencies(t *testing.T) {
	for _, mode := range []string{"gitdir", "alternates", "commondir"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, ".git")
			body := "gitdir: /external/repository"
			if mode != "gitdir" {
				file = filepath.Join(root, ".git", "objects", "info", mode)
				body = "/external/objects"
				if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := fingerprintTree(t.Context(), root); err == nil {
				t.Fatal("external git dependency was cacheable")
			}
		})
	}
}

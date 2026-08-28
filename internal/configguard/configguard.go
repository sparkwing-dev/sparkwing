package configguard

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

func WatchedFiles(home string) []string {
	return []string{
		filepath.Join(home, ".config", "sparkwing", "repos.yaml"),
		filepath.Join(home, ".config", "sparkwing", "profiles.yaml"),
	}
}

func SandboxLeaks(sandboxHome string) ([]string, error) {
	var leaks []string
	for _, root := range []string{
		filepath.Join(sandboxHome, ".config", "sparkwing"),
		filepath.Join(sandboxHome, ".sparkwing"),
	} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() {
				leaks = append(leaks, p)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", root, err)
		}
	}
	sort.Strings(leaks)
	return leaks, nil
}

func Fingerprint(home string) (map[string]string, error) {
	out := make(map[string]string, len(WatchedFiles(home)))
	for _, p := range WatchedFiles(home) {
		sum, err := fingerprintFile(p)
		if err != nil {
			return nil, err
		}
		out[p] = sum
	}
	return out, nil
}

func Diff(before, after map[string]string) []string {
	var changed []string
	for p, b := range before {
		if a, ok := after[p]; !ok || a != b {
			changed = append(changed, fmt.Sprintf("%s: %s -> %s", p, b, after[p]))
		}
	}
	sort.Strings(changed)
	return changed
}

func fingerprintFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "<absent>", nil
		}
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return fmt.Sprintf("sha256=%s,size=%d,mtime=%d",
		hex.EncodeToString(h.Sum(nil))[:16], fi.Size(), fi.ModTime().UnixNano()), nil
}

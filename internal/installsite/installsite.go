package installsite

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
)

func ExeName() string {
	if runtime.GOOS == "windows" {
		return "sparkwing.exe"
	}
	return "sparkwing"
}

func Self() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return normalize(exe), nil
}

func PathKey(resolved string) string {
	sum := sha256.Sum256([]byte(resolved))
	return hex.EncodeToString(sum[:8])
}

func normalize(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

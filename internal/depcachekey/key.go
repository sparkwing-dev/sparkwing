package depcachekey

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Files hashes workspace-contained inputs and their declaration for the current platform.
func Files(workdir, name, scope string, files []string) (string, error) {
	root, err := os.OpenRoot(workdir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for _, file := range files {
		if !filepath.IsLocal(file) {
			return "", fmt.Errorf("cache key file must be workspace-relative")
		}
	}
	for _, file := range files {
		data, err := root.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read cache key input: %w", err)
		}
		return Bytes(name, scope, data)
	}
	return "", fmt.Errorf("cache key input: %w", os.ErrNotExist)
}

// Bytes hashes captured cache-key input and its declaration for the current platform.
func Bytes(name, scope string, data []byte) (string, error) {
	raw, err := json.Marshal([]string{name, scope, runtime.GOOS, runtime.GOARCH})
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	digest.Write(raw)
	digest.Write([]byte{0})
	digest.Write(data)
	return fmt.Sprintf("dep-%x", digest.Sum(nil)), nil
}

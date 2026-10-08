package fssecure

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MkdirPrivateTemp creates a uniquely named owner-only directory without a
// permissive creation window on platforms whose mode bits do not set ACLs.
func MkdirPrivateTemp(parent, prefix string) (string, error) {
	if parent == "" {
		parent = os.TempDir()
	}
	if prefix == "." || prefix == ".." || filepath.Base(prefix) != prefix || strings.ContainsAny(prefix, "*?") {
		return "", fmt.Errorf("private temporary directory prefix %q must be one path component", prefix)
	}
	return mkdirPrivateTemp(parent, prefix)
}

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func usePipelineToolPath(t *testing.T) {
	t.Helper()
	var dirs []string
	for _, tool := range []string{"go", "git", "sh", "sed"} {
		if path, err := exec.LookPath(tool); err == nil {
			dir := filepath.Dir(path)
			if !slices.Contains(dirs, dir) {
				dirs = append(dirs, dir)
			}
		}
	}
	t.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
}

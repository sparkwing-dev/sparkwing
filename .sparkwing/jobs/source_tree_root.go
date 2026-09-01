package jobs

import (
	"os"
	"path/filepath"
	"runtime"
)

func sourceTreeRoot() string {
	if cwd, err := os.Getwd(); err == nil {
		if root := findSourceTreeRoot(cwd); root != "" {
			return root
		}
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(file) {
		return ""
	}
	return findSourceTreeRoot(filepath.Dir(file))
}

func findSourceTreeRoot(dir string) string {
	for {
		if isSourceTreeRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isSourceTreeRoot(dir string) bool {
	for _, marker := range []string{"AGENTS.md", filepath.Join(".sparkwing", "go.mod")} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err != nil {
			return false
		}
	}
	return true
}

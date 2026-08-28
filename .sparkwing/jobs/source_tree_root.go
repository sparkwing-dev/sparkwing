package jobs

import (
	"os"
	"path/filepath"
	"runtime"
)

func sourceTreeRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Dir(file)
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

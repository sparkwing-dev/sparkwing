package jobs

import (
	"os"
	"path/filepath"
	"runtime"
)

// sourceTreeRoot returns the root of the checkout this pipeline module was
// compiled from, found by walking up from this file's own compile-time
// path until a directory carries both of the repository's markers. It
// returns "" when no such directory is found, which is the answer a
// caller has to handle rather than paper over.
//
// It exists so a gate's own test can be pointed at the real repository.
// A whole-tree gate -- one that judges every tracked file rather than a
// staged diff -- is only meaningfully verified against a real tree, and
// the tree that matters is this one. Three separate authors needed that
// and each hand-wrote a throwaway test with their worktree's absolute
// path baked in: green in the checkout it was written in, broken in
// every other, and deleted before it could guard anything.
//
// runtime.Caller is the source because it is the only one that survives
// every way these tests are invoked. `cd .sparkwing && go test ./...`
// runs them with the working directory at the module, the pre-commit
// gate's test step runs them with the run's working directory bound
// elsewhere, and an editor runs them from wherever it likes; the path
// the compiler recorded does not move.
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

// isSourceTreeRoot reports whether dir holds the two files that only this
// repository's root holds together: the harness guidance every agent
// reads first, and the pipeline module this package lives in.
func isSourceTreeRoot(dir string) bool {
	for _, marker := range []string{"AGENTS.md", filepath.Join(".sparkwing", "go.mod")} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err != nil {
			return false
		}
	}
	return true
}

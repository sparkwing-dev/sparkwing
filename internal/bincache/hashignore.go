package bincache

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// HashAllFilesEnv disables gitignore-aware exclusion from the pipeline
// cache key when set to a non-empty value, restoring the older behavior
// of hashing every file under the module and its local replace targets.
//
// The escape hatch exists for one case: a build-affecting file that git
// ignores, such as a generated asset pulled in by //go:embed. Such a
// file is already invisible to teammates and to CI, so the healthy fix
// is to track it or to hash it explicitly the way .resolved.mod is --
// but the override keeps a broken tree buildable in the meantime.
const HashAllFilesEnv = "SPARKWING_HASH_ALL_FILES"

// ignoredUnder returns the subset of candidates that git ignores, as a
// set keyed by the same absolute paths that were passed in.
//
// Excluding ignored files is what makes a cache key portable. A build
// directory accumulates untracked local debris -- provider plugins,
// release outputs, coverage data -- that no build reads and that differs
// on every machine. Hashing it makes the key machine-specific by
// construction, so two checkouts of the same commit can never share a
// compiled binary. .gitignore, by contrast, is committed: every checkout
// and every CI runner computes the same exclusion.
//
// A dir outside any repository, a missing git, or any other failure
// yields an empty set, so the caller falls back to hashing everything.
// That is the conservative direction: a spurious cache miss costs one
// recompile, while a spurious hit would serve a stale binary.
func ignoredUnder(dir string, candidates []string) map[string]bool {
	if len(candidates) == 0 || os.Getenv(HashAllFilesEnv) != "" {
		return nil
	}

	// -z on both sides: a path may contain any byte except NUL, so
	// newline framing would corrupt unusual filenames.
	cmd := exec.Command("git", "check-ignore", "-z", "--stdin")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(strings.Join(candidates, "\x00") + "\x00")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		// Exit 1 means "nothing matched", which is a normal answer and
		// leaves out empty. Anything else -- git absent, dir outside a
		// repository -- also lands here and ignores nothing.
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return nil
		}
	}

	ignored := make(map[string]bool)
	for _, p := range strings.Split(out.String(), "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	return ignored
}

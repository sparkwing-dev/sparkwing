// Package gitenv carries a git hook's index binding across the unbinding that
// keeps it from reaching the commands a hook runs.
//
// A hook inherits GIT_INDEX_FILE naming the index the commit is being built
// in. Leaving it bound lets any git command a hook runs write into that
// commit, so it is unbound; but a check that wants to see the staged content
// still needs it. Unbind records the path under [GateIndexVar], and GateIndex
// hands it back to the one command that should see it.
package gitenv

import (
	"os"
	"path/filepath"
	"strings"
)

var bindingVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_PREFIX",
	"GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_NAMESPACE",
	"GIT_QUARANTINE_PATH",
}

// GateIndexVar names the environment variable holding the hook's index path
// after [Unbind] has removed the binding git itself acts on.
const GateIndexVar = "SPARKWING_GATE_INDEX"

// Unbind records the hook's index path under [GateIndexVar] and removes every
// variable git binds a working context with, so a command run from here reads
// the repository rather than the commit under construction.
func Unbind() {
	if index := os.Getenv("GIT_INDEX_FILE"); index != "" {
		if abs, err := filepath.Abs(index); err == nil {
			index = abs
		}
		_ = os.Setenv(GateIndexVar, index)
	}
	for _, name := range bindingVars {
		_ = os.Unsetenv(name)
	}
}

// GateIndex reports the recorded index path, or empty where there is none this
// process can stand behind. Bind it to the single command that should read the
// staged content rather than exporting it.
func GateIndex() string {
	path := os.Getenv(GateIndexVar)
	if path == "" {
		return ""
	}
	// #nosec G703 -- the gate index path comes from this process's own environment
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// ShellUnbind is Unbind as shell, for a hook that runs before this process
// does.
func ShellUnbind() string {
	return "if [ -n \"${GIT_INDEX_FILE:-}\" ]; then\n" +
		"\tcase \"$GIT_INDEX_FILE\" in\n" +
		"\t/*) " + GateIndexVar + "=\"$GIT_INDEX_FILE\" ;;\n" +
		"\t*) " + GateIndexVar + "=\"$(pwd)/$GIT_INDEX_FILE\" ;;\n" +
		"\tesac\n" +
		"\texport " + GateIndexVar + "\n" +
		"fi\n" +
		"unset " + strings.Join(bindingVars, " ") + "\n"
}

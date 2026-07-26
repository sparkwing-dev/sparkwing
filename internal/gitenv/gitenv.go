// Package gitenv keeps a process from staying bound to the git repository
// that launched it. Only a hook-launched sparkwing arrives bound, so this is
// the boundary between "a gate is running" and "the gate's own work".
package gitenv

import (
	"os"
	"path/filepath"
	"strings"
)

// bindingVars are the variables git exports to bind a process to one
// repository or worktree. git sets them for every hook it runs and the
// hook's whole process tree inherits them, so a step that means to work
// somewhere else does not: an inherited GIT_DIR outranks `git -C <path>`,
// and an inherited GIT_INDEX_FILE makes a plain `git add` stage into the
// hook's repository from anywhere on disk.
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

// GateIndexVar names the variable Unbind leaves behind carrying the index git
// was composing the gated commit in. It is namespaced instead of left as
// GIT_INDEX_FILE because git acts on that one ambiently -- every command in
// the pipeline would read and write through it, which is the corruption
// Unbind exists to stop. A step that means to inspect what is being committed
// asks for this one.
const GateIndexVar = "SPARKWING_GATE_INDEX"

// Unbind drops the repository-binding GIT_* variables from this process's
// environment, so every command it goes on to spawn -- and every command
// those spawn -- discovers a repository the way it would from a plain shell.
// Call it once, before any work, rather than per exec: a pipeline runs
// third-party tools that build their own environments, and only an unbound
// parent can protect those.
//
// GIT_INDEX_FILE is copied to GateIndexVar, as an absolute path, before it
// goes: git points a commit hook at the index it is building the commit in --
// .git/index.lock under `commit -a`, a next-index lock under a partial commit
// -- and that file, not the repository's index, holds what is about to be
// committed. Dropping it without a trace would show a staged-diff check an
// empty change and pass it. An already-set GateIndexVar is left alone, so the
// hook script's copy survives a second Unbind inside the same gate.
//
// Identity (GIT_AUTHOR_*, GIT_COMMITTER_*) and config selection
// (GIT_CONFIG_*) survive: they carry the operator's intent and name no
// repository, and a test fixture that isolates itself with GIT_CONFIG_GLOBAL
// depends on them reaching the git it runs.
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

// GateIndex returns the index file the gating commit is being built in, or ""
// when no hook bound this process to one. Callers pass it to a single git
// command as GIT_INDEX_FILE; exporting it to the whole process would put the
// ambient binding back.
//
// The path is checked before it is offered because git deletes that index when
// the commit finishes: a value inherited from an earlier gate names nothing,
// and a git pointed at a missing index reads an empty one, which is the silent
// pass this whole mechanism exists to avoid.
func GateIndex() string {
	path := os.Getenv(GateIndexVar)
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// ShellUnbind returns the POSIX sh statements that do Unbind's job somewhere
// Go cannot reach -- the hook script sparkwing writes, which has to protect a
// sparkwing on PATH older than the install that wrote it. Generating it from
// the same list is what keeps the two layers from drifting apart.
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

// Package engineenv names the SPARKWING_* variables the engine owns, which no
// process running pipeline code may inherit.
package engineenv

import (
	"os"
	"runtime"
	"slices"
	"strings"
)

// safety: credentials (tokens, the local secrets key), run options the host now holds, and plumbing the node
// protocol replaces. Every other SPARKWING_* name, a sparks setting, the caller's cache URLs or their own, reaches
// pipeline code as it did. A trailing _ matches a prefix; engineenv_test.go fails on a name left unclassified.
var names = []string{
	"_SPARKWING_", "SPARKWING_AGENT_TOKEN", "SPARKWING_CONTROLLER_URL", "SPARKWING_CACHE_GRANT",
	"SPARKWING_STANDALONE_REASON", "SPARKWING_STATE_DB", "SPARKWING_NODE_CLAIM_", "SPARKWING_LEASE_TOKEN", "SPARKWING_PROTOCOL_HANDLES",
	"SPARKWING_NODE_URL", "SPARKWING_NODE_TOKEN", "SPARKWING_RUN_ID", "SPARKWING_NODE_ID", "SPARKWING_START_AT",
	"SPARKWING_STOP_AT", "SPARKWING_ONLY", "SPARKWING_NO_CACHE", "SPARKWING_REF",
	"SPARKWING_ALLOW", "SPARKWING_MODE", "SPARKWING_WORKERS", "SPARKWING_DEBUG_PAUSE_",
	"SPARKWING_SECRETS_KEY", "SPARKWING_SECRETS_KEY_FILE",
	"SPARKWING_SECRETS_PREVIOUS_KEY", "SPARKWING_SOURCE_TOKEN", "SPARKWING_SOURCE_DIR",
	"SPARKWING_SOURCE_BUNDLE_OBJECT", "SPARKWING_WORKTREE_BASE_", "SPARKWING_RERUN", "SPARKWING_RERUN_REFS_DIR",
	"SPARKWING_FLEET_PARENT_",
}

// Owned reports whether key names a variable the engine owns, matching
// without regard to case as Windows reads variable names.
func Owned(key string) bool {
	key = strings.ToUpper(key)
	return slices.ContainsFunc(names, func(name string) bool {
		if strings.HasSuffix(name, "_") {
			return strings.HasPrefix(key, name)
		}
		return key == name
	})
}

// safety: the one list of engine variables a process inherits when its caller passes no environment, so a runner's
// own credentials, and options such as NODE_OPTIONS, never reach pipeline code or the installs it runs.
var minimal = []string{
	"PATH", "HOME", "TMPDIR", "LANG", "LC_*", "TZ", "USER",
	"SystemRoot", "ComSpec", "PATHEXT", "TEMP", "TMP",
}

// Minimal is the engine's environment cut to the variables a process needs
// to run at all: PATH, HOME, TMPDIR, LANG, LC_*, TZ and USER, and on Windows
// SystemRoot, ComSpec, PATHEXT, TEMP and TMP, plus the extra names, where a
// trailing * matches a prefix.
func Minimal(extra ...string) []string {
	keep := append(slices.Clone(minimal), extra...)
	env := []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if slices.ContainsFunc(keep, func(name string) bool {
			prefix, wild := strings.CutSuffix(name, "*")
			if runtime.GOOS == "windows" {
				k, name, prefix = strings.ToUpper(k), strings.ToUpper(name), strings.ToUpper(prefix)
			}
			return k == name || (wild && strings.HasPrefix(k, prefix))
		}) {
			env = append(env, kv)
		}
	}
	return env
}

// Strip returns env without the variables the engine owns.
func Strip(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !Owned(k) {
			out = append(out, kv)
		}
	}
	return out
}

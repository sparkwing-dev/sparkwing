// Package engineenv names the SPARKWING_* variables the engine owns, which no
// process running pipeline code may inherit.
package engineenv

import (
	"slices"
	"strings"
)

// safety: credentials (tokens, the local secrets key), run options the host now holds, and plumbing the node
// protocol replaces. Every other SPARKWING_* name, a sparks setting, the caller's cache URLs or their own, reaches
// pipeline code as it did. A trailing _ matches a prefix; engineenv_test.go fails on a name left unclassified.
var names = []string{
	"_SPARKWING_", "SPARKWING_AGENT_TOKEN", "SPARKWING_CONTROLLER_URL", "SPARKWING_LOGS_URL",
	"SPARKWING_API_SOCKET", "SPARKWING_PARENT_LIVENESS_FD", "SPARKWING_CACHE_GRANT", "SPARKWING_RUN_HANDLE_FILE",
	"SPARKWING_STANDALONE_REASON", "SPARKWING_STATE_DB", "SPARKWING_NODE_CLAIM_", "SPARKWING_NODE_SPEC_HASH",
	"SPARKWING_BROKERED_", "SPARKWING_EXECUTION_CAPABILITY", "SPARKWING_EXECUTION_CAPABILITY_STDIN",
	"SPARKWING_LEASE_TOKEN", "SPARKWING_CHILD_LEASE_TOKEN", "SPARKWING_MASK_VALUES_FD", "SPARKWING_PROTOCOL_HANDLES",
	"SPARKWING_NODE_URL", "SPARKWING_NODE_TOKEN", "SPARKWING_RUN_ID", "SPARKWING_NODE_ID", "SPARKWING_START_AT",
	"SPARKWING_STOP_AT", "SPARKWING_ONLY", "SPARKWING_NO_CACHE", "SPARKWING_REF",
	"SPARKWING_ALLOW", "SPARKWING_MODE", "SPARKWING_WORKERS", "SPARKWING_PRIORITY",
	"SPARKWING_LOG_LEVEL", "SPARKWING_PIPELINE_REV", "SPARKWING_DEBUG_PAUSE_",
	"SPARKWING_PROFILE", "SPARKWING_SECRETS_PROFILE", "SPARKWING_SECRETS_KEY", "SPARKWING_SECRETS_KEY_FILE",
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

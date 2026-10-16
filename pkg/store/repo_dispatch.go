package store

import "strings"

// RepoDispatch names the path a repository's new runs take.
type RepoDispatch string

const (
	// RepoDispatchTrigger is every repository's default: a runner claims the
	// run's trigger and dispatches its nodes. A repository with no setting,
	// or one stored empty, takes it too.
	RepoDispatchTrigger RepoDispatch = "trigger"
	// RepoDispatchController starts each run with a planning node, and the
	// launcher runs every node in a Job it builds itself.
	RepoDispatchController RepoDispatch = "controller"
)

// RepoKey is the key a repository's settings are stored under: its GitHub
// owner and name, lowercased because GitHub resolves both case-insensitively.
func RepoKey(owner, name string) string {
	return strings.ToLower(owner + "/" + name)
}

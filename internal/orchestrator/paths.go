// Package orchestrator runs pipelines declared via the sparkwing SDK.
// The local backend dispatches jobs as goroutines in the current
// process and persists run state to SQLite at ~/.sparkwing/.
package orchestrator

import "github.com/sparkwing-dev/sparkwing/internal/paths"

// Paths, DefaultPaths, and PathsAt are re-exports of the standalone
// internal/paths package. The canonical home is internal/paths;
// thin binaries (logs, web, controller) import that directly so
// they don't transitively link the dispatch engine. Code inside
// the orchestrator package keeps using these aliases unchanged.

// Paths resolves on-disk locations under the sparkwing home root.
type Paths = paths.Paths

// DefaultPaths returns paths rooted at ~/.sparkwing, honoring
// SPARKWING_HOME when set.
func DefaultPaths() (Paths, error) { return paths.DefaultPaths() }

// PathsAt roots the file layout at a specific directory.
func PathsAt(root string) Paths { return paths.PathsAt(root) }

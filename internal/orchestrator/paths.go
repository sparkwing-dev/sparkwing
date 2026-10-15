package orchestrator

import "github.com/sparkwing-dev/sparkwing/internal/paths"

type Paths = paths.Paths

func PathsAt(root string) Paths { return paths.PathsAt(root) }

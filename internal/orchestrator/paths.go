package orchestrator

import "github.com/sparkwing-dev/sparkwing/internal/paths"

type Paths = paths.Paths

func DefaultPaths() (Paths, error) { return paths.DefaultPaths() }

func PathsAt(root string) Paths { return paths.PathsAt(root) }

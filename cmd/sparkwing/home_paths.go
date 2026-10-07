package main

import "github.com/sparkwing-dev/sparkwing/internal/paths"

func homePaths(home string) (paths.Paths, error) {
	if home != "" {
		return paths.PathsAt(home), nil
	}
	return paths.DefaultPaths()
}

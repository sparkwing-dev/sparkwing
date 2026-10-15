package jobs

import "github.com/sparkwing-dev/sparkwing/sparkwing"

func pipelineCacheDir(tool string) string { return sparkwing.Path(".sparkwing", "cache", tool) }

func lintCache() sparkwing.DirCache {
	return sparkwing.Dir(".sparkwing/cache/golangci-lint", sparkwing.KeyFromFile("go.sum"))
}

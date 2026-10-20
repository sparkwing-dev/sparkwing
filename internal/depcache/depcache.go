// Package depcache extracts dependency-cache archives into a directory a host
// opened as an os.Root.
package depcache

// Resolver names a cache directory the host finds with the ecosystem's own
// tool, where the node runs.
const (
	ResolverGoModules = "go-modules"
	ResolverNpm       = "npm"
)

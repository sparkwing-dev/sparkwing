// Package sparks resolves sparks library dependencies declared in a
// consumer repo's .sparkwing/sparkwing.yaml `sparks:` section and
// materializes an overlay modfile (.sparkwing/.resolved.mod +
// .resolved.sum) used by the compile step via `go build -modfile=...`.
//
// The consumer's git-tracked go.mod and go.sum are never modified by this
// package. Callers load the manifest (projectconfig.LoadSparksManifest)
// and feed it the consumer sparkwing dir; the overlay lands beside it.
package sparks

// Manifest is the sparks: section of .sparkwing/sparkwing.yaml.
type Manifest struct {
	Libraries []Library `yaml:"libraries"`
}

// Library is one entry in the sparks manifest.
type Library struct {
	// Name is the logical name; matches spark.json:name. Advisory only
	// for the resolver; Source drives module resolution.
	Name string `yaml:"name"`
	// Source is the Go module path (e.g. github.com/sparkwing-dev/sparks-core).
	Source string `yaml:"source"`
	// Version is "latest", a semver range ("^v0.10.0", "~v0.10.0",
	// ">=v0.10.0") or an exact tag ("v0.10.3").
	Version string `yaml:"version"`
}

// Package scaffold exposes constants shared between the `sparkwing init`
// scaffolder and the tooling that keeps the scaffold honest.
package scaffold

// FallbackSDKVersion is the SDK version pinned into a fresh
// .sparkwing/go.mod when the running CLI's own version can't be detected
// (a source build with no release ldflag stamp). Source-built binaries
// hit this path, so the value must name an actual published tag on
// github.com/sparkwing-dev/sparkwing that is recent enough for the
// built-in templates (which use Job.Verify, Failure, ...) to compile
// against.
//
// Keep it current: the version-freshness gate (CheckVersionsFreshness)
// fails when this pin falls behind the latest released SDK, so a fresh
// scaffold never emits a go.mod that can't build the current templates.
const FallbackSDKVersion = "v0.17.3"

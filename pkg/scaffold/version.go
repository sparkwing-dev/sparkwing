// Package scaffold exposes constants shared between the `sparkwing init`
// scaffolder and the tooling that keeps the scaffold honest.
package scaffold

// FallbackSDKVersion is the published Go SDK release pinned by new pipelines.
// Engine releases retain this pin until a separately published SDK changes it.
const FallbackSDKVersion = "v0.1.0"

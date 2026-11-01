//go:build race

// Package race reports whether the binary was built with the race detector,
// so a test can size work that the detector makes many times slower.
package race

// Enabled is true in a binary built with -race.
const Enabled = true

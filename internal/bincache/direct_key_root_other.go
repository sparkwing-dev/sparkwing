//go:build !linux

package bincache

// safety: Off Linux, no tmpfs root is known to this runner.
func isTmpfs(string) bool { return false }

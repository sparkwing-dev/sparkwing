//go:build !linux

package bincache

// isTmpfs is false off Linux, where the runner knows no tmpfs to trust.
func isTmpfs(string) bool { return false }

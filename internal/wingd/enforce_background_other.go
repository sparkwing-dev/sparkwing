//go:build !darwin

package wingd

// backgroundProcess is a no-op off macOS. Linux enforces the budget with
// a cgroup wall instead; other platforms leave the admission cap as the
// sole constraint.
func backgroundProcess(int) error { return nil }

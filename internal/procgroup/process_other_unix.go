//go:build !windows && !darwin

package procgroup

// nativeProcessTable reports that this platform has no kernel process
// listing of its own, so the portable `ps` reader answers instead.
func nativeProcessTable(bool) ([]Info, bool) { return nil, false }

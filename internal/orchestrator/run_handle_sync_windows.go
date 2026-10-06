//go:build windows

package orchestrator

// hack: Windows cannot sync directories; the file is synced before its link is published.
func syncRunHandleDirectory(string) error { return nil }

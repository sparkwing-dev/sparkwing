//go:build windows

package orchestrator

// hack: Windows cannot sync directories; snapshot bytes are synced before publication.
func syncSubmissionEnvironmentDirectory(string) error { return nil }

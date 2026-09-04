package orchestrator

import (
	"strings"
	"testing"
)

func TestFleetSourceSnapshotProjectionContainsNoLocalPathOrCredential(t *testing.T) {
	payload := string(fleetSourceSnapshotPayload(Options{
		FleetSourceSHA: "abcdef", FleetSourceFiles: 7,
		FleetSourceBytes: 1234, FleetBundleBytes: 567,
		FleetSourceRoot: "/private/secret/worktree", FleetSourceRepoURL: "https://credential@example.test/repo.git",
	}))
	for _, want := range []string{`"commit":"abcdef"`, `"files":7`, `"source_bytes":1234`, `"bundle_bytes":567`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("projection %s lacks %s", payload, want)
		}
	}
	for _, forbidden := range []string{"/private/secret", "credential", "repo.git"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("projection %s exposes %q", payload, forbidden)
		}
	}
}

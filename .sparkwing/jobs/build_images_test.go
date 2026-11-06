package jobs

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestServiceBuildStampMatchesOnlyDescribeOfAStableTag(t *testing.T) {
	for tag, want := range map[string]bool{
		"v0.68.6-0-g0123456789ab": true, "v0.68.6-14-g0123456789ab": true,
		"v0.68.6": false, "commit-0123456": false, "v0.68.6-qa.1-3-g0123456789ab": false,
		"v0.68.6-3-g0123456789ab-dirty": false, "v0.68.6-03-g0123456789ab": false, "v0.68.6-3-gABCDEF012345": false,
	} {
		if serviceBuildStamp.MatchString(tag) != want {
			t.Errorf("serviceBuildStamp(%q) = %v, want %v", tag, !want, want)
		}
	}
}

func TestResolveServiceBuildUsesCanonicalPseudoVersion(t *testing.T) {
	version, err := resolveServiceBuild(t.Context(), func(_ context.Context, args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "rev-parse --is-shallow-repository":
			return "false", nil
		case "fetch --quiet --tags origin":
			return "", nil
		case "tag --merged HEAD --list v*":
			return "v0.69.1\nv0.69.2\nv0.69.3-qa.1\nv0.69.02\n", nil
		case "show -s --format=%H%n%cI HEAD":
			return "718a7591f23a" + strings.Repeat("0", 28) + "\n2026-11-05T21:10:46-07:00", nil
		case "rev-parse refs/tags/v0.69.2^{commit}":
			return strings.Repeat("a", 40), nil
		default:
			return "", fmt.Errorf("unexpected git call: %v", args)
		}
	})
	if err != nil || version != "v0.69.3-0.20261106041046-718a7591f23a" {
		t.Fatalf("main version = %q, %v", version, err)
	}
}

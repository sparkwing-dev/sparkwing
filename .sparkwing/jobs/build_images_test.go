package jobs

import "testing"

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

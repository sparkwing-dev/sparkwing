package wingwire

import "testing"

// TestMinVersionSpeakingProtocolMajor_MatchesTheReleaseThatBumpedIt guards the
// pair: the constant is only meaningful while it names the release carrying
// the current ProtocolMajor, and nothing else in the build ties them together.
func TestMinVersionSpeakingProtocolMajor_MatchesTheReleaseThatBumpedIt(t *testing.T) {
	if ProtocolMajor != 2 {
		t.Fatalf("ProtocolMajor = %d; move MinVersionSpeakingProtocolMajor to the release that introduced it and update this test", ProtocolMajor)
	}
	if MinVersionSpeakingProtocolMajor != "v0.22.0" {
		t.Fatalf("MinVersionSpeakingProtocolMajor = %q, want v0.22.0", MinVersionSpeakingProtocolMajor)
	}
}

func TestSpeaksCurrentProtocol_SplitsAtTheBoundaryRelease(t *testing.T) {
	behind := []string{"v0.15.4", "v0.16.1", "v0.17.0", "v0.17.25", "v0.18.0", "v0.19.0", "v0.20.0"}
	for _, v := range behind {
		if SpeaksCurrentProtocol(v) {
			t.Errorf("SpeaksCurrentProtocol(%q) = true; that release speaks protocol 1", v)
		}
	}
	current := []string{"v0.22.0", "v0.23.0", "v1.0.0"}
	for _, v := range current {
		if !SpeaksCurrentProtocol(v) {
			t.Errorf("SpeaksCurrentProtocol(%q) = false; that release is at or past the boundary", v)
		}
	}
}

// TestSpeaksCurrentProtocol_TreatsAPrereleaseOfTheBoundaryAsBehind pins the
// semver rule that cost a night of misdiagnosis: v0.22.0-dev sorts below
// v0.22.0, so a source build of the boundary release does not satisfy it.
func TestSpeaksCurrentProtocol_TreatsAPrereleaseOfTheBoundaryAsBehind(t *testing.T) {
	if SpeaksCurrentProtocol("v0.22.0-dev+1c4a5646") {
		t.Error("a -dev prerelease of the boundary release must not count as speaking it")
	}
}

// TestSpeaksCurrentProtocol_StaysSilentOnUnreadableVersions keeps callers from
// accusing a repo whose pin could not be parsed.
func TestSpeaksCurrentProtocol_StaysSilentOnUnreadableVersions(t *testing.T) {
	for _, v := range []string{"", "latest", "not-a-version"} {
		if !SpeaksCurrentProtocol(v) {
			t.Errorf("SpeaksCurrentProtocol(%q) = false; an unreadable version is not evidence of being behind", v)
		}
	}
}

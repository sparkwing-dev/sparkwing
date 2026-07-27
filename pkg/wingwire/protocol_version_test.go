package wingwire

import (
	"testing"

	"golang.org/x/mod/semver"
)

func TestReleasedProtocolFloors_CoverEveryMajorUpToTheCurrentOne(t *testing.T) {
	floors := ReleasedProtocolFloors()
	if len(floors) == 0 {
		t.Fatal("the floor table must name at least protocol 1")
	}
	for i, f := range floors {
		if f.Major != i+1 {
			t.Fatalf("floor %d is major %d; majors must run 1..n with no gaps", i, f.Major)
		}
		if !semver.IsValid(f.MinVersion) {
			t.Fatalf("floor for major %d is %q, which is not semver", f.Major, f.MinVersion)
		}
		if i > 0 && semver.Compare(f.MinVersion, floors[i-1].MinVersion) <= 0 {
			t.Fatalf("floor for major %d is %q, which does not sort above major %d's %q",
				f.Major, f.MinVersion, floors[i-1].Major, floors[i-1].MinVersion)
		}
	}
	if newest := floors[len(floors)-1].Major; newest != ProtocolMajor {
		t.Fatalf("newest floor is major %d but ProtocolMajor is %d; a bump appends a row naming the release that carries it", newest, ProtocolMajor)
	}
}

func TestMajorSpokenBy_SplitsReleasesAtEveryFloor(t *testing.T) {
	floors := ProtocolFloors{
		{Major: 1, MinVersion: "v0.0.0"},
		{Major: 2, MinVersion: "v0.22.0"},
		{Major: 3, MinVersion: "v0.30.0"},
	}
	want := map[string]int{
		"v0.15.4":  1,
		"v0.17.25": 1,
		"v0.20.0":  1,
		"v0.22.0":  2,
		"v0.23.0":  2,
		"v0.29.9":  2,
		"v0.30.0":  3,
		"v1.0.0":   3,
	}
	for version, wantMajor := range want {
		if major, ok := floors.MajorSpokenBy(version); !ok || major != wantMajor {
			t.Errorf("MajorSpokenBy(%q) = %d, %v; want %d, true", version, major, ok, wantMajor)
		}
	}
}

// A release past the newest floor reports that floor's major: a table cannot
// describe a major first released after it was written.
func TestMajorSpokenBy_ReportsTheNewestKnownMajorForLaterReleases(t *testing.T) {
	major, ok := ReleasedProtocolFloors().MajorSpokenBy("v99.0.0")
	if !ok || major != ProtocolMajor {
		t.Fatalf("MajorSpokenBy(v99.0.0) = %d, %v; want %d, true", major, ok, ProtocolMajor)
	}
}

func TestMajorSpokenBy_TreatsAPrereleaseOfAFloorAsTheMajorBelow(t *testing.T) {
	major, ok := ReleasedProtocolFloors().MajorSpokenBy("v0.22.0-dev+1c4a5646")
	if !ok || major != 1 {
		t.Fatalf("MajorSpokenBy(v0.22.0-dev) = %d, %v; want 1, true -- a prerelease sorts below its release", major, ok)
	}
}

func TestMajorSpokenBy_RefusesToGuessAtUnreadableVersions(t *testing.T) {
	for _, v := range []string{"", "latest", "not-a-version"} {
		if major, ok := ReleasedProtocolFloors().MajorSpokenBy(v); ok {
			t.Errorf("MajorSpokenBy(%q) = %d, true; an unreadable version is not evidence of any major", v, major)
		}
	}
}

func TestMinVersionSpeaking_NamesTheReleaseThatIntroducedAKnownMajor(t *testing.T) {
	got, ok := ReleasedProtocolFloors().MinVersionSpeaking(2)
	if !ok || got != "v0.22.0" {
		t.Fatalf("MinVersionSpeaking(2) = %q, %v; want v0.22.0, true", got, ok)
	}
}

func TestMinVersionSpeaking_HasNoAnswerForAMajorTheTablePredates(t *testing.T) {
	got, ok := ReleasedProtocolFloors().MinVersionSpeaking(ProtocolMajor + 1)
	if ok || got != "" {
		t.Fatalf("MinVersionSpeaking(%d) = %q, %v; want an empty string and false", ProtocolMajor+1, got, ok)
	}
}

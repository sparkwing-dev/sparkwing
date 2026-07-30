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

// Every release below a major's floor speaks a major below it, and every
// release at or above it speaks that major or newer. That ordering is what
// lets a caller holding a peer's major place a pin with one comparison.
func TestMinVersionSpeaking_SortsEveryReleaseOntoTheRightSideOfAFloor(t *testing.T) {
	floors := ProtocolFloors{
		{Major: 1, MinVersion: "v0.0.0"},
		{Major: 2, MinVersion: "v0.22.0"},
		{Major: 3, MinVersion: "v0.30.0"},
	}
	floor, ok := floors.MinVersionSpeaking(3)
	if !ok {
		t.Fatal("MinVersionSpeaking(3) has no answer for a major the table carries")
	}
	below := []string{"v0.15.4", "v0.17.25", "v0.22.0", "v0.23.0", "v0.29.9", "v0.30.0-dev+1c4a5646"}
	atOrAbove := []string{"v0.30.0", "v0.31.2", "v1.0.0"}
	for _, v := range below {
		if semver.Compare(v, floor) >= 0 {
			t.Errorf("%q sorts at or above the protocol-3 floor %q; it speaks an older major", v, floor)
		}
	}
	for _, v := range atOrAbove {
		if semver.Compare(v, floor) < 0 {
			t.Errorf("%q sorts below the protocol-3 floor %q; it speaks protocol 3", v, floor)
		}
	}
}

func TestNewest_NamesTheHighestMajorTheTableCovers(t *testing.T) {
	floor, ok := ReleasedProtocolFloors().Newest()
	if !ok {
		t.Fatal("the shipped table has no newest row")
	}
	if floor.Major != ProtocolMajor {
		t.Fatalf("newest row is major %d; this build speaks %d", floor.Major, ProtocolMajor)
	}
}

func TestNewest_HasNoAnswerForAnEmptyTable(t *testing.T) {
	if floor, ok := (ProtocolFloors{}).Newest(); ok {
		t.Fatalf("Newest() = %+v, true; an empty table covers no major", floor)
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

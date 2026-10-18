package runspec

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestBurstPlacementValidation(t *testing.T) {
	for _, steps := range [][]PlacementStep{nil, {{Target: "here", Hold: -1}}, {{Target: "unknown"}}, {{Target: "name="}}, {{Target: "pool=x,y"}}, {{Target: "here"}, {Target: "here"}}, {{Target: "here", Hold: time.Duration(math.MaxInt64)}, {Target: "cloud", Hold: 1}}} {
		if err := (BurstPlacement{Preferences: steps}).Validate(); err == nil {
			t.Fatalf("accepted %+v", steps)
		}
	}
	for _, value := range []int64{-1, math.MaxInt64} {
		if err := (BurstPlacement{Preferences: []PlacementStep{{Target: "cloud"}}, OverflowMinutes: &value}).Validate(); err == nil {
			t.Fatalf("accepted limit %d", value)
		}
	}
	for _, target := range []string{"here", "machines", "cloud", "name=desktop", "pool=linux"} {
		if err := (BurstPlacement{Preferences: []PlacementStep{{Target: target}}}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBurstPlacementIsImmutableInRequestSeal(t *testing.T) {
	limit := int64(30)
	request := Request{Version: 1, Team: "a", Pipeline: "ci", Mode: IntakeExecute, Dispatch: DispatchHost, Git: Git{Repository: Repository{Key: "gh:1"}, SHA: strings.Repeat("a", 40)}, Source: Source{Ref: strings.Repeat("a", 40)}, Burst: &BurstPlacement{Preferences: []PlacementStep{{Target: "here", Hold: time.Second}, {Target: "cloud"}}, OverflowMinutes: &limit}}
	seal, err := SealRequest(request, IntakeFacts{RunID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	request.Burst.Preferences[0].Target = "cloud"
	limit = 60
	if err := verifyRequest(seal); err != nil {
		t.Fatal(err)
	}
	if seal.Request.Burst.Preferences[0].Target != "here" || *seal.Request.Burst.OverflowMinutes != 30 {
		t.Fatal("request seal shares mutable placement")
	}
}

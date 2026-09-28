package match_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestMatchLabels_EmptyNeededMatchesAnything(t *testing.T) {
	cases := []struct {
		name string
		have []string
	}{
		{name: "nil", have: nil},
		{name: "empty", have: []string{}},
		{name: "populated", have: []string{"linux", "amd64"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !matchLabels(nil, tc.have) {
				t.Errorf("nil needed should match %v", tc.have)
			}
			if !matchLabels([]string{}, tc.have) {
				t.Errorf("empty needed should match %v", tc.have)
			}
		})
	}
}

func TestMatchLabels_AndAcrossTerms(t *testing.T) {
	cases := []struct {
		name   string
		needed []string
		have   []string
		want   bool
	}{
		{name: "all-present", needed: []string{"linux", "amd64"}, have: []string{"linux", "amd64"}, want: true},
		{name: "subset", needed: []string{"linux", "amd64"}, have: []string{"linux"}, want: false},
		{name: "superset", needed: []string{"linux"}, have: []string{"linux", "amd64", "extra"}, want: true},
		{name: "disjoint", needed: []string{"linux"}, have: []string{"darwin"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchLabels(tc.needed, tc.have)
			if got != tc.want {
				t.Errorf("MatchLabels(%v, %v) = %v, want %v", tc.needed, tc.have, got, tc.want)
			}
		})
	}
}

func TestMatchLabels_CommaOrWithinTerm(t *testing.T) {
	cases := []struct {
		name   string
		needed []string
		have   []string
		want   bool
	}{
		{name: "first-alt", needed: []string{"os=linux,os=macos"}, have: []string{"os=linux"}, want: true},
		{name: "second-alt", needed: []string{"os=linux,os=macos"}, have: []string{"os=macos"}, want: true},
		{name: "no-alt", needed: []string{"os=linux,os=macos"}, have: []string{"os=windows"}, want: false},
		{name: "bare-or", needed: []string{"gpu,fpga"}, have: []string{"fpga"}, want: true},
		{name: "bare-or-neither", needed: []string{"gpu,fpga"}, have: []string{"cpu"}, want: false},
		{name: "trims-whitespace", needed: []string{"linux , macos"}, have: []string{"macos"}, want: true},
		{name: "ignores-empty-alts", needed: []string{"linux,,macos"}, have: []string{"macos"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchLabels(tc.needed, tc.have)
			if got != tc.want {
				t.Errorf("MatchLabels(%v, %v) = %v, want %v", tc.needed, tc.have, got, tc.want)
			}
		})
	}
}

func TestMatchLabels_MixedAndOr(t *testing.T) {
	needed := []string{"os=linux,os=macos", "arch=amd64"}
	cases := []struct {
		name string
		have []string
		want bool
	}{
		{name: "linux+amd64", have: []string{"os=linux", "arch=amd64"}, want: true},
		{name: "macos+amd64", have: []string{"os=macos", "arch=amd64"}, want: true},
		{name: "linux-only", have: []string{"os=linux"}, want: false},
		{name: "amd64-only", have: []string{"arch=amd64"}, want: false},
		{name: "linux+arm64", have: []string{"os=linux", "arch=arm64"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchLabels(needed, tc.have)
			if got != tc.want {
				t.Errorf("MatchLabels(%v, %v) = %v, want %v", needed, tc.have, got, tc.want)
			}
		})
	}
}

// safety: the dispatcher's own check, over labels it trusts as they stand.
func matchLabels(needed, have []string) bool {
	return match.Evaluate(match.Profile{Labels: have}, match.Demand{Selector: needed}).OK()
}

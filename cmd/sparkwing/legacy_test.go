package main

import "testing"

func TestLegacyWarningLine(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, ""},
		{-1, ""},
		{1, "1 legacy-pinned pipeline running outside daemon admission -- bump their sparkwing pins"},
		{3, "3 legacy-pinned pipelines running outside daemon admission -- bump their sparkwing pins"},
	}
	for _, tc := range cases {
		if got := legacyWarningLine(tc.n); got != tc.want {
			t.Errorf("legacyWarningLine(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestLiveLegacyBoxSlots_EmptyHomeHasNone(t *testing.T) {
	live, err := liveLegacyBoxSlots(t.TempDir())
	if err != nil {
		t.Fatalf("liveLegacyBoxSlots: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("live = %+v, want none for an empty home", live)
	}
}

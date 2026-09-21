package main

import (
	"slices"
	"testing"
)

func TestDispatchModeEnv_WorkerCapDoesNotNeedAMode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		workers int
		want    []string
	}{
		{name: "cap alone", workers: 3, want: []string{"SPARKWING_WORKERS=3"}},
		{name: "mode alone", mode: "ci-embedded", want: []string{"SPARKWING_MODE=ci-embedded"}},
		{
			name: "both", mode: "ci-embedded", workers: 4,
			want: []string{"SPARKWING_MODE=ci-embedded", "SPARKWING_WORKERS=4"},
		},
		{name: "neither"},
		{name: "zero workers is no cap", workers: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := dispatchModeEnv(tc.mode, tc.workers); !slices.Equal(got, tc.want) {
				t.Errorf("dispatchModeEnv(%q, %d) = %v, want %v", tc.mode, tc.workers, got, tc.want)
			}
		})
	}
}

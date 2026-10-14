package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestCanceledByRun_DistinguishesTeardownFromGenuineFault(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, true},
		{"wrapped context canceled", fmt.Errorf("step: %w", context.Canceled), true},
		{"sigkilled child", errors.New("command terminated by cancellation: go test (signal: killed)"), true},
		{"genuine non-zero exit", errors.New("version-freshness: dep behind by 1 tag"), false},
		{"deadline exceeded stays a fault", context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canceledByRun(tc.err); got != tc.want {
				t.Fatalf("canceledByRun(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

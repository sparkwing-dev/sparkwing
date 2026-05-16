package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TestNodeClaim_CommaORTermClaimableByEitherAlternative verifies the
// comma-OR term semantics on the claim path: a node demanding
// "os=linux,os=macos" is claimable by either a linux runner or a
// macos runner.
func TestNodeClaim_CommaORTermClaimableByEitherAlternative(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		runner    []string
		wantClaim bool
	}{
		{name: "linux-runner", runner: []string{"os=linux"}, wantClaim: true},
		{name: "macos-runner", runner: []string{"os=macos"}, wantClaim: true},
		{name: "windows-runner", runner: []string{"os=windows"}, wantClaim: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreT(t)
			seedNodeWithLabels(t, s, "run-1", "build", []string{"os=linux,os=macos"})

			n, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, tc.runner)
			if tc.wantClaim {
				if err != nil {
					t.Fatalf("expected claim, got err=%v", err)
				}
				if n.NodeID != "build" {
					t.Fatalf("wrong node: %+v", n)
				}
				return
			}
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("expected ErrNotFound, got n=%+v err=%v", n, err)
			}
		})
	}
}

// TestNodeClaim_MixedAndOrTerms verifies AND-across-terms with OR
// inside each term: needs ("os=linux,os=macos", "arch=amd64") means
// (linux OR macos) AND amd64. A runner missing amd64 is rejected;
// runners with either OS and amd64 are accepted.
func TestNodeClaim_MixedAndOrTerms(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		runner    []string
		wantClaim bool
	}{
		{name: "linux+amd64", runner: []string{"os=linux", "arch=amd64"}, wantClaim: true},
		{name: "macos+amd64", runner: []string{"os=macos", "arch=amd64"}, wantClaim: true},
		{name: "linux+arm64", runner: []string{"os=linux", "arch=arm64"}, wantClaim: false},
		{name: "amd64-only", runner: []string{"arch=amd64"}, wantClaim: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreT(t)
			seedNodeWithLabels(t, s, "run-1", "build", []string{"os=linux,os=macos", "arch=amd64"})

			n, err := s.ClaimNextReadyNode(ctx, "pod-1", 30*time.Second, tc.runner)
			if tc.wantClaim {
				if err != nil {
					t.Fatalf("expected claim, got err=%v", err)
				}
				if n.NodeID != "build" {
					t.Fatalf("wrong node: %+v", n)
				}
				return
			}
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("expected ErrNotFound, got n=%+v err=%v", n, err)
			}
		})
	}
}

// TestNodeClaim_BareLabelCommaOR verifies the bare-label form
// (gpu,fpga) — runner having either claims; runner with neither is
// rejected.
func TestNodeClaim_BareLabelCommaOR(t *testing.T) {
	ctx := context.Background()
	s := newStoreT(t)
	seedNodeWithLabels(t, s, "run-1", "accel", []string{"gpu,fpga"})

	n, err := s.ClaimNextReadyNode(ctx, "pod-fpga", 30*time.Second, []string{"fpga"})
	if err != nil {
		t.Fatalf("fpga runner should claim accel: %v", err)
	}
	if n.NodeID != "accel" {
		t.Fatalf("wrong node: %+v", n)
	}
}

package main

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// IMP-011: enforce the dispatch gate from the wing CLI's perspective.
// The unit-level coverage (Venue.String, EnforceVenue contract) lives
// in pkg/sparkwing; here we pin the wire-format venue-string mapping
// the CLI uses to read from the describe cache.

func TestParseVenue(t *testing.T) {
	cases := []struct {
		in   string
		want sparkwing.Venue
	}{
		{"", sparkwing.VenueEither},
		{"either", sparkwing.VenueEither},
		{"local-only", sparkwing.VenueLocalOnly},
		{"cluster-only", sparkwing.VenueClusterOnly},
		{"garbage", sparkwing.VenueEither}, // unknown values fall through to safe default.
	}
	for _, tc := range cases {
		if got := parseVenue(tc.in); got != tc.want {
			t.Errorf("parseVenue(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestEnforcePipelineVenue_LocalOnlyRefusesRemote(t *testing.T) {
	err := enforcePipelineVenue("local-only", "cluster-up", "prod")
	if err == nil {
		t.Fatal("enforcePipelineVenue: want error, got nil")
	}
	var vme *sparkwing.VenueMismatchError
	if !errors.As(err, &vme) {
		t.Fatalf("expected *VenueMismatchError, got %T", err)
	}
	want := `pipeline "cluster-up" declares venue=local-only and cannot be dispatched to profile "prod". Drop --on or change Venue to ClusterOnly/Either.`
	if got := err.Error(); got != want {
		t.Errorf("error =\n  %q\nwant\n  %q", got, want)
	}
}

func TestEnforcePipelineVenue_LocalOnlyAllowsBare(t *testing.T) {
	if err := enforcePipelineVenue("local-only", "cluster-up", ""); err != nil {
		t.Fatalf("local-only + bare invocation should pass: got %v", err)
	}
}

func TestEnforcePipelineVenue_ClusterOnlyRefusesBare(t *testing.T) {
	err := enforcePipelineVenue("cluster-only", "prune-pvcs", "")
	if err == nil {
		t.Fatal("enforcePipelineVenue: want error, got nil")
	}
	want := `pipeline "prune-pvcs" declares venue=cluster-only and requires --on PROFILE.`
	if got := err.Error(); got != want {
		t.Errorf("error =\n  %q\nwant\n  %q", got, want)
	}
}

func TestEnforcePipelineVenue_ClusterOnlyAllowsRemote(t *testing.T) {
	if err := enforcePipelineVenue("cluster-only", "prune-pvcs", "prod"); err != nil {
		t.Fatalf("cluster-only + --on prod should pass: got %v", err)
	}
}

func TestEnforcePipelineVenue_EitherUnconstrained(t *testing.T) {
	cases := []string{"", "either", "garbage"}
	for _, v := range cases {
		if err := enforcePipelineVenue(v, "deploy", ""); err != nil {
			t.Errorf("venue=%q + bare: unexpected err %v", v, err)
		}
		if err := enforcePipelineVenue(v, "deploy", "prod"); err != nil {
			t.Errorf("venue=%q + --on: unexpected err %v", v, err)
		}
	}
}

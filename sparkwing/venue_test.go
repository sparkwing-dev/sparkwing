package sparkwing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// venueResetRegistry purges the package-level pipeline registry so
// tests can Register without leaking state across runs. Mirrors the
// pattern used in pipeline_await_test.go for the same reason.
func venueResetRegistry(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	registry = map[string]*Registration{}
	registryMu.Unlock()
}

func TestVenueString(t *testing.T) {
	cases := []struct {
		v    Venue
		want string
	}{
		{VenueEither, "either"},
		{VenueLocalOnly, "local-only"},
		{VenueClusterOnly, "cluster-only"},
		{Venue(99), "either"}, // unknown values fall through to the safe default.
	}
	for _, tc := range cases {
		if got := tc.v.String(); got != tc.want {
			t.Errorf("Venue(%d).String() = %q, want %q", tc.v, got, tc.want)
		}
	}
}

// noVenuePipeline doesn't implement Venue(); PipelineVenue must
// return VenueEither.
type noVenuePipeline struct{ Base }

func (noVenuePipeline) Plan(ctx context.Context, plan *Plan, in NoInputs, rc RunContext) error {
	return nil
}

// localOnlyPipeline opts into VenueLocalOnly.
type localOnlyPipeline struct{ Base }

func (localOnlyPipeline) Plan(ctx context.Context, plan *Plan, in NoInputs, rc RunContext) error {
	return nil
}
func (localOnlyPipeline) Venue() Venue { return VenueLocalOnly }

// clusterOnlyPipeline opts into VenueClusterOnly.
type clusterOnlyPipeline struct{ Base }

func (clusterOnlyPipeline) Plan(ctx context.Context, plan *Plan, in NoInputs, rc RunContext) error {
	return nil
}
func (clusterOnlyPipeline) Venue() Venue { return VenueClusterOnly }

// venueRegisterOnce wraps Register in a sync.Once-keyed lock so the
// per-test registry reset is the only mechanism that needs to clear
// state.
var venueRegisterMu sync.Mutex

func TestPipelineVenue(t *testing.T) {
	venueRegisterMu.Lock()
	defer venueRegisterMu.Unlock()
	venueResetRegistry(t)

	Register[NoInputs]("venue-none", func() Pipeline[NoInputs] { return noVenuePipeline{} })
	Register[NoInputs]("venue-local", func() Pipeline[NoInputs] { return localOnlyPipeline{} })
	Register[NoInputs]("venue-cluster", func() Pipeline[NoInputs] { return clusterOnlyPipeline{} })

	cases := []struct {
		name string
		want Venue
	}{
		{"venue-none", VenueEither},
		{"venue-local", VenueLocalOnly},
		{"venue-cluster", VenueClusterOnly},
	}
	for _, tc := range cases {
		reg, ok := Lookup(tc.name)
		if !ok {
			t.Fatalf("Lookup(%q): not registered", tc.name)
		}
		if got := PipelineVenue(reg); got != tc.want {
			t.Errorf("PipelineVenue(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Nil-safety: PipelineVenue(nil) must return VenueEither rather
	// than panicking. Callers (e.g. dispatchers reading from a stale
	// describe cache) shouldn't have to nil-check.
	if got := PipelineVenue(nil); got != VenueEither {
		t.Errorf("PipelineVenue(nil) = %v, want VenueEither", got)
	}
}

func TestEnforceVenue(t *testing.T) {
	cases := []struct {
		name     string
		venue    Venue
		pipeline string
		on       string
		wantErr  bool
		wantMsg  string
	}{
		{name: "either-bare", venue: VenueEither, pipeline: "x", on: "", wantErr: false},
		{name: "either-on", venue: VenueEither, pipeline: "x", on: "prod", wantErr: false},
		{name: "local-bare", venue: VenueLocalOnly, pipeline: "x", on: "", wantErr: false},
		{name: "local-on", venue: VenueLocalOnly, pipeline: "cluster-up", on: "prod",
			wantErr: true,
			wantMsg: `pipeline "cluster-up" declares venue=local-only and cannot be dispatched to profile "prod". Drop --on or change Venue to ClusterOnly/Either.`,
		},
		{name: "cluster-on", venue: VenueClusterOnly, pipeline: "x", on: "prod", wantErr: false},
		{name: "cluster-bare", venue: VenueClusterOnly, pipeline: "prune-pvcs", on: "",
			wantErr: true,
			wantMsg: `pipeline "prune-pvcs" declares venue=cluster-only and requires --on PROFILE.`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := EnforceVenue(tc.venue, tc.pipeline, tc.on)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("EnforceVenue: want error, got nil")
				}
				if got := err.Error(); got != tc.wantMsg {
					t.Errorf("EnforceVenue error =\n  %q\nwant\n  %q", got, tc.wantMsg)
				}
				var vme *VenueMismatchError
				if !errors.As(err, &vme) {
					t.Errorf("EnforceVenue: error is not *VenueMismatchError: %T", err)
				}
			} else if err != nil {
				t.Fatalf("EnforceVenue: unexpected error: %v", err)
			}
		})
	}
}

func TestVenueMismatchErrorTypeAssert(t *testing.T) {
	err := EnforceVenue(VenueLocalOnly, "deploy", "prod")
	var vme *VenueMismatchError
	if !errors.As(err, &vme) {
		t.Fatalf("expected *VenueMismatchError, got %T", err)
	}
	if vme.Pipeline != "deploy" || vme.Venue != VenueLocalOnly || vme.On != "prod" {
		t.Errorf("VenueMismatchError fields wrong: %+v", vme)
	}
	if vme.Reason != "remote-dispatch" {
		t.Errorf("Reason = %q, want %q", vme.Reason, "remote-dispatch")
	}
	if !strings.Contains(err.Error(), "local-only") {
		t.Errorf("error message missing venue label: %q", err.Error())
	}
}

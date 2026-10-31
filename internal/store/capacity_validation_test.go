package store_test

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestPipelineProfile_InvalidObservationLeavesProfileUnchanged(t *testing.T) {
	for name, invalid := range map[string]store.ProfileObservation{
		"negative duration":       {Duration: -time.Nanosecond},
		"negative peak CPU":       {PeakCores: -1},
		"negative memory":         {PeakMemoryBytes: -1},
		"negative sustained CPU":  {SustainedCores: -1},
		"nonfinite peak CPU":      {PeakCores: math.NaN()},
		"nonfinite sustained CPU": {SustainedCores: math.Inf(1)},
	} {
		for _, state := range []string{"absent", "existing"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				st := storetest.Open(t)
				ctx := context.Background()
				if state == "existing" {
					if err := tenantFor(t, st, store.DefaultTeam).RecordProfileObservation(ctx, "validation", "build", store.ProfileObservation{
						Duration: time.Second, PeakCores: 1, SustainedCores: 0.5,
						PeakMemoryBytes: 64 << 20, CPUMeasured: true,
					}); err != nil {
						t.Fatal(err)
					}
				}
				before, err := tenantFor(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "validation", "build")
				if err != nil || (before != nil) != (state == "existing") {
					t.Fatalf("initial profile: %v, %v", before, err)
				}
				if err := tenantFor(t, st, store.DefaultTeam).RecordProfileObservation(ctx, "validation", "build", invalid); err == nil {
					t.Error("invalid observation was accepted")
				}
				after, err := tenantFor(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "validation", "build")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Errorf("invalid observation changed profile: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}

func TestPipelineProfile_ZeroObservationIsValid(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := tenantFor(t, st, store.DefaultTeam).RecordProfileObservation(ctx, "zero", "build", store.ProfileObservation{
		SustainedCores: 0, CPUMeasured: true,
	}); err != nil {
		t.Fatal(err)
	}
	profile, err := tenantFor(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "zero", "build")
	if err != nil || profile == nil {
		t.Fatalf("profile: %v, %v", profile, err)
	}
	if profile.SampleCount != 1 || !profile.CPUMeasured || profile.SustainedCores != 0 || profile.PeakCores != 0 || profile.PeakMemoryBytes != 0 {
		t.Fatalf("measured zero was not retained: %+v", profile)
	}
}

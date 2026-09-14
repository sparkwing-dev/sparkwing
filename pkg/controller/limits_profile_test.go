package controller_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestLimitsProfile_NoneSuppliesNothing(t *testing.T) {
	got, err := controller.LimitsProfile("")
	if err != nil {
		t.Fatalf("LimitsProfile(\"\"): %v", err)
	}
	if got != (controller.LimitsProfileValues{}) {
		t.Errorf("an install that names no profile got %+v; every guard must stay unlimited", got)
	}
}

func TestLimitsProfile_HostedProfilesCarryTheDocumentedValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		want controller.LimitsProfileValues
	}{
		{
			name: controller.LimitsProfileCloud,
			want: controller.LimitsProfileValues{
				ClaimsPerRunnerMinute:     9600,
				HeartbeatsPerRunnerMinute: 1200,
				MaxLogStreamsPerPrincipal: 50,
				MaxDownloadsPerPrincipal:  20,
				EnforceIdleClaimPoll:      true,
			},
		},
		{
			name: controller.LimitsProfileCloudFree,
			want: controller.LimitsProfileValues{
				ClaimsPerRunnerMinute:     2400,
				HeartbeatsPerRunnerMinute: 600,
				MaxLogStreamsPerPrincipal: 10,
				MaxDownloadsPerPrincipal:  5,
				EnforceIdleClaimPoll:      true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := controller.LimitsProfile(tc.name)
			if err != nil {
				t.Fatalf("LimitsProfile(%q): %v", tc.name, err)
			}
			if got != tc.want {
				t.Errorf("profile %q = %+v; want %+v", tc.name, got, tc.want)
			}
		})
	}
}

func TestLimitsProfile_ClaimBudgetsFollowTheRunnerCadence(t *testing.T) {
	paid, err := controller.LimitsProfile(controller.LimitsProfileCloud)
	if err != nil {
		t.Fatalf("LimitsProfile: %v", err)
	}
	if want := controller.RecommendedClaimsPerMinuteForSlots(8); paid.ClaimsPerRunnerMinute != want {
		t.Errorf("cloud claim budget = %d; an eight-slot agent needs %d",
			paid.ClaimsPerRunnerMinute, want)
	}
}

func TestLimitsProfile_UnknownNameIsRefused(t *testing.T) {
	_, err := controller.LimitsProfile("clod")
	if err == nil {
		t.Fatal("a misspelled profile started a controller with no guards at all")
	}
	for _, name := range controller.LimitsProfileNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name the profile %q", err, name)
		}
	}
}

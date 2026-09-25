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
				ClaimsPerRunnerMinute:     480,
				HeartbeatsPerRunnerMinute: 1200,
				RequestsPerTokenMinute:    2000,
				RequestsPerMinuteAlarm:    5000,
				MaxLogStreamsPerPrincipal: 50,
				MaxDownloadsPerPrincipal:  20,
				RunsPerPrincipalHour:      600,
				ShedQueueDepth:            5000,

				EgressMonthlyBytesPerPrincipal: 100 << 30,
				EgressDailyCapBytes:            200 << 30,
				EnforceIdleClaimPoll:           true,
			},
		},
		{
			name: controller.LimitsProfileCloudFree,
			want: controller.LimitsProfileValues{
				ClaimsPerRunnerMinute:     240,
				HeartbeatsPerRunnerMinute: 600,
				RequestsPerTokenMinute:    600,
				RequestsPerMinuteAlarm:    5000,
				MaxLogStreamsPerPrincipal: 10,
				MaxDownloadsPerPrincipal:  5,
				RunsPerPrincipalHour:      60,
				ShedQueueDepth:            1000,

				EgressMonthlyBytesPerPrincipal: 5 << 30,
				EgressDailyCapBytes:            20 << 30,
				EnforceIdleClaimPoll:           true,
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

// TestLimitsProfile_ClaimBudgetsClearTheCompliantCadence is the property the
// numbers have to keep however they are retuned: a runner keeping the cadence
// it was configured with is never refused, and the budget is a small multiple
// of that cadence rather than a round number that bounds nothing.
func TestLimitsProfile_ClaimBudgetsClearTheCompliantCadence(t *testing.T) {
	compliant := controller.CompliantClaimPollsPerMinute()
	for _, name := range controller.LimitsProfileNames() {
		profile, err := controller.LimitsProfile(name)
		if err != nil {
			t.Fatalf("LimitsProfile(%q): %v", name, err)
		}
		if profile.ClaimsPerRunnerMinute <= compliant {
			t.Errorf("%s claim budget = %d; a runner polling every %s spends %d a minute",
				name, profile.ClaimsPerRunnerMinute, controller.ClaimPollInterval, compliant)
		}
		if profile.ClaimsPerRunnerMinute > compliant*8 {
			t.Errorf("%s claim budget = %d; more than eight times the compliant %d bounds nothing",
				name, profile.ClaimsPerRunnerMinute, compliant)
		}
	}
}

// TestLimitsProfile_HostedControllersCarryEveryGuard is the provisioning check:
// a controller started under either hosted profile has every guard the hosted
// tiers are sold with set, so none of them can be left off by an edit that
// forgets one.
func TestLimitsProfile_HostedControllersCarryEveryGuard(t *testing.T) {
	for _, name := range []string{controller.LimitsProfileCloud, controller.LimitsProfileCloudFree} {
		profile, err := controller.LimitsProfile(name)
		if err != nil {
			t.Fatalf("LimitsProfile(%q): %v", name, err)
		}
		for _, guard := range []struct {
			name  string
			value int64
		}{
			{"claims per runner minute", int64(profile.ClaimsPerRunnerMinute)},
			{"heartbeats per runner minute", int64(profile.HeartbeatsPerRunnerMinute)},
			{"requests per token minute", int64(profile.RequestsPerTokenMinute)},
			{"requests per minute alarm", int64(profile.RequestsPerMinuteAlarm)},
			{"max log streams per principal", int64(profile.MaxLogStreamsPerPrincipal)},
			{"max downloads per principal", int64(profile.MaxDownloadsPerPrincipal)},
			{"runs per principal hour", int64(profile.RunsPerPrincipalHour)},
			{"shed queue depth", int64(profile.ShedQueueDepth)},
			{"egress monthly bytes per principal", profile.EgressMonthlyBytesPerPrincipal},
			{"egress daily cap bytes", profile.EgressDailyCapBytes},
		} {
			if guard.value <= 0 {
				t.Errorf("%s leaves %s unlimited", name, guard.name)
			}
		}
		if !profile.EnforceIdleClaimPoll {
			t.Errorf("%s does not enforce the idle claim poll", name)
		}
	}
}

// TestLimitsProfile_TheFreeTierIsTighterThanThePaidOne pins the ordering the
// tiers are priced on, so a retune cannot leave free the more generous.
func TestLimitsProfile_TheFreeTierIsTighterThanThePaidOne(t *testing.T) {
	paid, err := controller.LimitsProfile(controller.LimitsProfileCloud)
	if err != nil {
		t.Fatalf("LimitsProfile: %v", err)
	}
	free, err := controller.LimitsProfile(controller.LimitsProfileCloudFree)
	if err != nil {
		t.Fatalf("LimitsProfile: %v", err)
	}
	for _, tc := range []struct {
		name       string
		paid, free int64
	}{
		{"claims per runner minute", int64(paid.ClaimsPerRunnerMinute), int64(free.ClaimsPerRunnerMinute)},
		{"heartbeats per runner minute", int64(paid.HeartbeatsPerRunnerMinute), int64(free.HeartbeatsPerRunnerMinute)},
		{"requests per token minute", int64(paid.RequestsPerTokenMinute), int64(free.RequestsPerTokenMinute)},
		{"max log streams per principal", int64(paid.MaxLogStreamsPerPrincipal), int64(free.MaxLogStreamsPerPrincipal)},
		{"max downloads per principal", int64(paid.MaxDownloadsPerPrincipal), int64(free.MaxDownloadsPerPrincipal)},
		{"runs per principal hour", int64(paid.RunsPerPrincipalHour), int64(free.RunsPerPrincipalHour)},
		{"shed queue depth", int64(paid.ShedQueueDepth), int64(free.ShedQueueDepth)},
		{"egress monthly bytes per principal", paid.EgressMonthlyBytesPerPrincipal, free.EgressMonthlyBytesPerPrincipal},
		{"egress daily cap bytes", paid.EgressDailyCapBytes, free.EgressDailyCapBytes},
	} {
		if tc.free >= tc.paid {
			t.Errorf("%s: free %d is not below paid %d", tc.name, tc.free, tc.paid)
		}
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

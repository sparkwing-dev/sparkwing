package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func TestApplyLimitsProfile_AnInstallThatSetsNothingIsUnchanged(t *testing.T) {
	none, err := controller.LimitsProfile("")
	if err != nil {
		t.Fatalf("LimitsProfile: %v", err)
	}
	if got := applyLimitsProfile(none, guardValues{}, guardsNamed{}); got != (guardValues{}) {
		t.Errorf("guards = %+v; a controller given no profile must serve what it served before", got)
	}
}

func TestApplyLimitsProfile_CloudTurnsEveryGuardOn(t *testing.T) {
	cloud, err := controller.LimitsProfile(controller.LimitsProfileCloud)
	if err != nil {
		t.Fatalf("LimitsProfile: %v", err)
	}
	want := guardValues{
		ClaimsPerRunnerMinute:     480,
		HeartbeatsPerRunnerMinute: 1200,
		RequestsPerTokenMinute:    2000,
		RequestsPerMinuteAlarm:    5000,
		MaxLogStreamsPerPrincipal: 50,
		MaxDownloadsPerPrincipal:  20,
		RunsPerPrincipalHour:      600,
		ShedQueueDepth:            5000,
		EgressMonthlyBytes:        100 << 30,
		EgressDailyCapBytes:       200 << 30,
		EnforceIdleClaimPoll:      true,
	}
	if got := applyLimitsProfile(cloud, guardValues{}, guardsNamed{}); got != want {
		t.Errorf("guards = %+v; want %+v", got, want)
	}
}

func TestApplyLimitsProfile_WhatTheOperatorNamedWins(t *testing.T) {
	cloud, err := controller.LimitsProfile(controller.LimitsProfileCloud)
	if err != nil {
		t.Fatalf("LimitsProfile: %v", err)
	}
	set := guardValues{
		ClaimsPerRunnerMinute:     120,
		HeartbeatsPerRunnerMinute: 60,
		RequestsPerTokenMinute:    900,
		RequestsPerMinuteAlarm:    100,
		MaxLogStreamsPerPrincipal: 2,
		MaxDownloadsPerPrincipal:  1,
		RunsPerPrincipalHour:      7,
		ShedQueueDepth:            8,
		EgressMonthlyBytes:        9,
		EgressDailyCapBytes:       10,
	}
	got := applyLimitsProfile(cloud, set, guardsNamed{
		ClaimsPerRunnerMinute:     true,
		HeartbeatsPerRunnerMinute: true,
		RequestsPerTokenMinute:    true,
		RequestsPerMinuteAlarm:    true,
		MaxLogStreamsPerPrincipal: true,
		MaxDownloadsPerPrincipal:  true,
		RunsPerPrincipalHour:      true,
		ShedQueueDepth:            true,
		EgressMonthlyBytes:        true,
		EgressDailyCapBytes:       true,
	})
	set.EnforceIdleClaimPoll = true
	if got != set {
		t.Errorf("guards = %+v; want the operator's own values %+v", got, set)
	}
}

// TestApplyLimitsProfile_AnExplicitZeroStaysUnlimited covers the documented
// meaning of zero: a guard the operator turned off by naming zero stays off
// under a profile, because an explicit value always wins.
func TestApplyLimitsProfile_AnExplicitZeroStaysUnlimited(t *testing.T) {
	cloud, err := controller.LimitsProfile(controller.LimitsProfileCloud)
	if err != nil {
		t.Fatalf("LimitsProfile: %v", err)
	}
	got := applyLimitsProfile(cloud, guardValues{}, guardsNamed{ClaimsPerRunnerMinute: true})
	if got.ClaimsPerRunnerMinute != 0 {
		t.Errorf("claims per runner minute = %d; an explicit zero is documented unlimited", got.ClaimsPerRunnerMinute)
	}
	if got.HeartbeatsPerRunnerMinute != cloud.HeartbeatsPerRunnerMinute {
		t.Errorf("heartbeats = %d; a guard the operator left alone takes the profile's value",
			got.HeartbeatsPerRunnerMinute)
	}
}

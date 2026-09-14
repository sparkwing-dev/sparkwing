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
	if got := applyLimitsProfile(none, guardValues{}); got != (guardValues{}) {
		t.Errorf("guards = %+v; a controller given no profile must serve what it served before", got)
	}
}

func TestApplyLimitsProfile_CloudTurnsEveryGuardOn(t *testing.T) {
	cloud, err := controller.LimitsProfile(controller.LimitsProfileCloud)
	if err != nil {
		t.Fatalf("LimitsProfile: %v", err)
	}
	want := guardValues{
		ClaimsPerRunnerMinute:     9600,
		HeartbeatsPerRunnerMinute: 1200,
		MaxLogStreamsPerPrincipal: 50,
		MaxDownloadsPerPrincipal:  20,
		EnforceIdleClaimPoll:      true,
	}
	if got := applyLimitsProfile(cloud, guardValues{}); got != want {
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
		MaxLogStreamsPerPrincipal: 2,
		MaxDownloadsPerPrincipal:  1,
	}
	got := applyLimitsProfile(cloud, set)
	set.EnforceIdleClaimPoll = true
	if got != set {
		t.Errorf("guards = %+v; want the operator's own values %+v", got, set)
	}
}

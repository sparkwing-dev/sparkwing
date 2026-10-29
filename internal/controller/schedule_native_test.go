//go:build !windows

package controller

import "testing"

func TestNativeLocalControllerScheduleFiresOneSealedRunAtDueInstant(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.5s measured in the short class; the full suite runs it")
	}
	checkLocalControllerScheduledIntake(t, true)
}

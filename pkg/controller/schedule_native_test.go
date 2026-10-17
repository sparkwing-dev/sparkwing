//go:build !windows

package controller

import "testing"

func TestNativeLocalControllerScheduleFiresOneSealedRunAtDueInstant(t *testing.T) {
	checkLocalControllerScheduledIntake(t, true)
}

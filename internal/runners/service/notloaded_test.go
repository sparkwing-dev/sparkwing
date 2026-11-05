package service

import "testing"

func TestNotLoadedRecognisesLaunchctlMissingService(t *testing.T) {
	for _, out := range []string{
		"Bad request.\nCould not find service \"com.sparkwing.runner\" in domain for user gui: 502",
		"Could not find specified service",
		"Unit sparkwing-runner.service not loaded.",
	} {
		if !notLoaded(out) {
			t.Errorf("notLoaded(%q) = false, want true", out)
		}
	}
	if notLoaded("Operation not permitted") {
		t.Error("notLoaded treated a permission error as a missing service")
	}
}

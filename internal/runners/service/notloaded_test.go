package service

import (
	"os"
	"testing"
)

func TestInstallDarwinTreatsAMissingAgentAsNotInstalled(t *testing.T) {
	exec := &fakeExec{fail: map[string]string{
		"launchctl print gui/501/com.sparkwing.runner": "Bad request.\nCould not find service \"com.sparkwing.runner\" in domain for user gui: 501",
	}}
	h := darwinHost(t, exec)
	state, err := Install(t.Context(), h)
	if err != nil {
		t.Fatalf("Install on a Mac without the agent: %v", err)
	}
	if !state.Installed {
		t.Fatalf("state = %+v, want installed", state)
	}
	if _, err := os.Stat(plistPath(h)); err != nil {
		t.Fatalf("plist was not written: %v", err)
	}
}

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

// safety: the user config directory is disposable here, because the behavior
// under test is a write reaching the operator's real one.
func scratchUserConfig(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("SPARKWING_PROFILES", "")
	return filepath.Join(xdg, "sparkwing", "profiles.yaml")
}

func TestProfilesAddUnderAScratchHomeLeavesTheUserConfigUntouched(t *testing.T) {
	userProfiles := scratchUserConfig(t)
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)

	operatorHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve the operator home: %v", err)
	}
	before, err := configguard.Fingerprint(operatorHome)
	if err != nil {
		t.Fatalf("fingerprint %s: %v", operatorHome, err)
	}

	err = runProfilesAdd([]string{"--name", "drill", "--controller", "http://127.0.0.1:4344"})
	if !errors.Is(err, profile.ErrOutsideSandboxHome) {
		t.Fatalf("profiles add under SPARKWING_HOME=%s returned %v, want ErrOutsideSandboxHome", home, err)
	}

	if _, statErr := os.Stat(userProfiles); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want the user config left absent", userProfiles, statErr)
	}
	after, err := configguard.Fingerprint(operatorHome)
	if err != nil {
		t.Fatalf("fingerprint %s: %v", operatorHome, err)
	}
	if changed := configguard.Diff(before, after); len(changed) > 0 {
		t.Errorf("a command run under a scratch home changed the operator's config: %v", changed)
	}
}

func TestProfilesAddWritesInsideTheHomeItIsPointedAt(t *testing.T) {
	userProfiles := scratchUserConfig(t)
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	inHome := filepath.Join(home, "profiles.yaml")
	t.Setenv("SPARKWING_PROFILES", inHome)

	if err := runProfilesAdd([]string{"--name", "drill", "--controller", "http://127.0.0.1:4344"}); err != nil {
		t.Fatalf("profiles add with SPARKWING_PROFILES=%s: %v", inHome, err)
	}

	if p := loadSavedProfile(t, inHome, "drill"); p.Controller == nil || p.Controller.URL != "http://127.0.0.1:4344" {
		t.Errorf("profile written to %s = %+v, want the controller it was given", inHome, p.Controller)
	}
	if _, err := os.Stat(userProfiles); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want the user config left absent", userProfiles, err)
	}
}

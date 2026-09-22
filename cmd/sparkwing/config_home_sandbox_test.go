package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/fleet"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// safety: the user config directory is disposable here, because the behavior
// under test is a write reaching the operator's real one. Every per-file
// override is cleared, so each command resolves the path it would in a shell.
func scratchUserConfigDir(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	for _, env := range []string{
		profile.PathEnv, repos.PathEnv, fleet.PathEnv,
		secrets.SecretsPathEnv, secrets.ConfigPathEnv, versionHoldEnv,
	} {
		t.Setenv(env, "")
	}
	return filepath.Join(xdg, "sparkwing")
}

// safety: the refusal is only half the claim. The other half is that the
// operator's own config came through the command untouched, which a
// fingerprint taken on both sides of the run is what proves.
func underAScratchHome(t *testing.T) string {
	t.Helper()
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
	t.Cleanup(func() {
		after, err := configguard.Fingerprint(operatorHome)
		if err != nil {
			t.Errorf("fingerprint %s: %v", operatorHome, err)
			return
		}
		if changed := configguard.Diff(before, after); len(changed) > 0 {
			t.Errorf("a command run under a scratch home changed the operator's config: %v", changed)
		}
	})
	return home
}

func wantRefusedAndUnwritten(t *testing.T, what string, err error, path string) {
	t.Helper()
	if !errors.Is(err, configguard.ErrOutsideSandboxHome) {
		t.Fatalf("%s under a scratch home returned %v, want ErrOutsideSandboxHome", what, err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want the user config left absent", path, statErr)
	}
}

func TestProfilesAddUnderAScratchHomeLeavesTheUserConfigUntouched(t *testing.T) {
	userConfig := scratchUserConfigDir(t)
	underAScratchHome(t)

	err := runProfilesAdd([]string{"--name", "drill", "--controller", "http://127.0.0.1:4344"})
	wantRefusedAndUnwritten(t, "profiles add", err, filepath.Join(userConfig, "profiles.yaml"))
}

func TestProfilesAddWritesInsideTheHomeItIsPointedAt(t *testing.T) {
	userConfig := scratchUserConfigDir(t)
	home := underAScratchHome(t)
	inHome := filepath.Join(home, "profiles.yaml")
	t.Setenv(profile.PathEnv, inHome)

	if err := runProfilesAdd([]string{"--name", "drill", "--controller", "http://127.0.0.1:4344"}); err != nil {
		t.Fatalf("profiles add with %s=%s: %v", profile.PathEnv, inHome, err)
	}

	if p := loadSavedProfile(t, inHome, "drill"); p.Controller == nil || p.Controller.URL != "http://127.0.0.1:4344" {
		t.Errorf("profile written to %s = %+v, want the controller it was given", inHome, p.Controller)
	}
	userProfiles := filepath.Join(userConfig, "profiles.yaml")
	if _, err := os.Stat(userProfiles); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want the user config left absent", userProfiles, err)
	}
}

func TestSecretSetUnderAScratchHomeLeavesTheUserConfigUntouched(t *testing.T) {
	userConfig := scratchUserConfigDir(t)
	underAScratchHome(t)

	err := runSecretSet([]string{"--name", "DRILL_TOKEN", "--value", "drill"})
	wantRefusedAndUnwritten(t, "secret set", err, filepath.Join(userConfig, "secrets.env"))

	err = runSecretSet([]string{"--name", "DRILL_REGION", "--value", "us-east-1", "--plain"})
	wantRefusedAndUnwritten(t, "secret set --plain", err, filepath.Join(userConfig, "config.env"))
}

func TestSecretDeleteUnderAScratchHomeLeavesTheUserConfigUntouched(t *testing.T) {
	userConfig := scratchUserConfigDir(t)
	underAScratchHome(t)

	err := runSecretDelete([]string{"--name", "DRILL_TOKEN"})
	wantRefusedAndUnwritten(t, "secret delete", err, filepath.Join(userConfig, "secrets.env"))
}

func TestSecretSetWritesInsideTheHomeItIsPointedAt(t *testing.T) {
	userConfig := scratchUserConfigDir(t)
	home := underAScratchHome(t)
	inHome := filepath.Join(home, "secrets.env")
	t.Setenv(secrets.SecretsPathEnv, inHome)

	if err := runSecretSet([]string{"--name", "DRILL_TOKEN", "--value", "drill"}); err != nil {
		t.Fatalf("secret set with %s=%s: %v", secrets.SecretsPathEnv, inHome, err)
	}

	entries, err := secrets.ListDotenvEntries(inHome)
	if err != nil {
		t.Fatalf("read %s: %v", inHome, err)
	}
	if entries["DRILL_TOKEN"] != "drill" {
		t.Errorf("secret written to %s = %q, want the value it was given", inHome, entries["DRILL_TOKEN"])
	}
	userSecrets := filepath.Join(userConfig, "secrets.env")
	if _, err := os.Stat(userSecrets); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want the user config left absent", userSecrets, err)
	}
}

func TestVersionHoldUnderAScratchHomeLeavesTheUserConfigUntouched(t *testing.T) {
	userConfig := scratchUserConfigDir(t)
	underAScratchHome(t)
	holdFile := filepath.Join(userConfig, "version-hold")

	err := runVersionHold([]string{"--set", "v0.15"})
	wantRefusedAndUnwritten(t, "version hold --set", err, holdFile)

	err = runVersionHold([]string{"--clear"})
	wantRefusedAndUnwritten(t, "version hold --clear", err, holdFile)
}

func TestFleetInitUnderAScratchHomeLeavesTheUserConfigUntouched(t *testing.T) {
	userConfig := scratchUserConfigDir(t)
	underAScratchHome(t)

	err := runFleetInit([]string{"--listen", "127.0.0.1:4346", "--public-url", "http://127.0.0.1:4346"})
	wantRefusedAndUnwritten(t, "fleet init", err, filepath.Join(userConfig, fleet.Filename))
}

func TestXrepoAddUnderAScratchHomeLeavesTheUserConfigUntouched(t *testing.T) {
	userConfig := scratchUserConfigDir(t)
	underAScratchHome(t)
	checkout := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatalf("make a checkout to register: %v", err)
	}

	err := runXrepoAdd([]string{checkout})
	wantRefusedAndUnwritten(t, "xrepo add", err, filepath.Join(userConfig, "repos.yaml"))
}

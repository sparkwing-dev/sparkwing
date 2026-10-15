package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestProfilesEditPreservesOtherMachineSettings(t *testing.T) {
	path := profilesFixturePath(t)
	if err := writePrivateConfigFixture(path, []byte("run: {cache_from: '', submit_env_allow: []}\ncache: {max_entries: 0}\nrunner: {repos: [], local_admission: false}\n")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "--profile", "prod", "--controller", "https://api.example", "--no-probe"},
		{"set", "--profile", "prod", "--token=new-credential"},
		{"remove", "--profile", "prod", "--keep-token"},
	} {
		captureStdout(t, func() {
			if err := runProfiles(args); err != nil {
				t.Fatal(err)
			}
		})
		cfg, err := userconfig.ReadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Run == nil || cfg.Run.CacheFrom == nil || *cfg.Run.CacheFrom != "" || cfg.Run.SubmitEnvAllow == nil || cfg.Cache == nil || cfg.Cache.MaxEntries == nil || *cfg.Cache.MaxEntries != 0 || cfg.Runner == nil || cfg.Runner.Repos == nil || cfg.Runner.LocalAdmission == nil || *cfg.Runner.LocalAdmission {
			t.Fatal("profile edit lost another section's explicit empty or zero setting")
		}
	}
}

func TestProfilesAddRetainsConnectionAddedDuringProbe(t *testing.T) {
	path := profilesFixturePath(t)
	withStdin(t, "private-credential\n")
	profileHTTPFixture(t, func(r *http.Request) (int, string) {
		if r.URL.Path == "/api/v1/health" {
			other := map[string]userconfig.Profile{"other": {Controller: userconfig.Connection{URL: "https://other.example"}}}
			if err := userconfig.Write(path, userconfig.Profiles, "profiles", other); err != nil {
				t.Fatal(err)
			}
			return 200, `{"status":"ok","auth":"enabled"}`
		}
		return 200, `{"runs":[],"next_cursor":"","truncated":false}`
	})
	captureStdout(t, func() {
		if err := runProfiles([]string{"add", "--profile", "prod", "--controller", "https://api.example", "--token-stdin"}); err != nil {
			t.Fatal(err)
		}
	})
	cfg, err := userconfig.ReadConfig(path)
	if err != nil || len(cfg.Profiles) != 2 || cfg.Profiles["other"].Controller.URL != "https://other.example" {
		t.Fatalf("lost concurrent profile addition: %v (%d profiles)", err, len(cfg.Profiles))
	}
}

func TestProfilesAddRechecksReplacementAfterProbe(t *testing.T) {
	path := profilesFixturePath(t)
	withStdin(t, "private-credential\n")
	profileHTTPFixture(t, func(r *http.Request) (int, string) {
		if r.URL.Path == "/api/v1/health" {
			other := map[string]userconfig.Profile{"prod": {Controller: userconfig.Connection{URL: "https://other.example"}}}
			if err := userconfig.Write(path, userconfig.Profiles, "profiles", other); err != nil {
				t.Fatal(err)
			}
			return 200, `{"status":"ok","auth":"enabled"}`
		}
		return 200, `{"runs":[],"next_cursor":"","truncated":false}`
	})
	err := runProfiles([]string{"add", "--profile", "prod", "--controller", "https://api.example", "--token-stdin"})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("concurrent replacement accepted: %v", err)
	}
	if got := loadSavedProfile(t, path, "prod").ControllerURL(); got != "https://other.example" {
		t.Fatal("overwrote concurrent profile")
	}
}

func TestProfilesRemoveRetainsConnectionChangedDuringRevocation(t *testing.T) {
	path := profilesFixturePath(t)
	token := "swk_12345678_private-credential"
	profiles := map[string]userconfig.Profile{"prod": {Controller: userconfig.Connection{URL: "https://api.example", Token: &token}}}
	if err := userconfig.Write(path, userconfig.Profiles, "profiles", profiles); err != nil {
		t.Fatal(err)
	}
	profileHTTPFixture(t, func(r *http.Request) (int, string) {
		prefix, err := tokenPrefix(token)
		if err != nil {
			t.Fatal(err)
		}
		if r.Method == "GET" {
			return 200, fmt.Sprintf(`{"prefix":%q,"kind":"member","principal":"person","scopes":[]}`, prefix)
		}
		profiles["prod"] = userconfig.Profile{Controller: userconfig.Connection{URL: "https://replacement.example"}}
		if err := userconfig.Write(path, userconfig.Profiles, "profiles", profiles); err != nil {
			t.Fatal(err)
		}
		return 200, fmt.Sprintf(`{"prefix":%q,"revoked":true}`, prefix)
	})
	err := runProfiles([]string{"remove", "--profile", "prod"})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("removed a changed connection: %v", err)
	}
	if got := loadSavedProfile(t, path, "prod").ControllerURL(); got != "https://replacement.example" {
		t.Fatal("lost replacement connection")
	}
}

func TestProfileCommandsRejectTheWholeInvalidMachineFile(t *testing.T) {
	for _, body := range []string{
		"profiles: {prod: {controller: {url: https://api.example}, state: {type: sqlite}}}",
		"profiles: {prod: {controller: {url: https://api.example}}}\nrunner: {allow_repos: [old]}",
		"profiles: {prod: {controller: {url: https://api.example, token: private-credential}, token: private-credential}}",
	} {
		path := profilesFixturePath(t)
		if err := writePrivateConfigFixture(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"show", "--profile", "prod"}, {"set", "--profile", "prod", "--token=new-credential"}, {"remove", "--profile", "prod", "--keep-token"}} {
			err := runProfiles(args)
			if err == nil {
				t.Fatalf("accepted invalid config through %s", args[0])
			}
			if strings.Contains(err.Error(), "private-credential") {
				t.Fatal("config error exposed credential")
			}
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != body {
			t.Fatal("refusal changed machine config")
		}
	}
}

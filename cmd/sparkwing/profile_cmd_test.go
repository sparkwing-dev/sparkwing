package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func setProfileCmdFixture(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := fssecure.WriteFile(path, []byte(body)); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
}

func TestProfileCmd_RequiresSelection(t *testing.T) {
	if err := runProfilesArgs([]string{"show"}); err == nil {
		t.Fatal("show accepted missing --profile")
	}
}

func TestProfileCmd_FlagSelectsConnection(t *testing.T) {
	setProfileCmdFixture(t, `
profiles:
  prod: { controller: { url: https://api.example.dev } }
  team: { controller: { url: https://team.example.dev } }
`)
	out := captureStdout(t, func() {
		if err := runProfilesArgs([]string{"show", "--profile", "team", "--output", "pretty"}); err != nil {
			t.Errorf("profile: %v", err)
		}
	})
	if !strings.Contains(out, "name:       team") {
		t.Errorf("expected team connection; got:\n%s", out)
	}
	if !strings.Contains(out, "controller:") {
		t.Errorf("expected controller; got:\n%s", out)
	}
}

func TestProfileCmd_NotFound(t *testing.T) {
	setProfileCmdFixture(t, `
profiles:
  prod: { controller: { url: https://api.example.dev } }
`)
	err := runProfilesArgs([]string{"show", "--profile", "bogus"})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), `profile "bogus" not found`) {
		t.Errorf("message = %q", err.Error())
	}
}

func TestProfileCmd_JSONConnectionName(t *testing.T) {
	setProfileCmdFixture(t, `
profiles:
  prod: { controller: { url: https://api.example.dev, token: swu_secret } }
`)
	out := captureStdout(t, func() {
		if err := runProfilesArgs([]string{"show", "--profile", "prod", "-o", "json"}); err != nil {
			t.Errorf("profile: %v", err)
		}
	})
	var report profileIndex
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not parseable JSON: %v\n%s", err, out)
	}
	if report.Name != "prod" {
		t.Errorf("name = %q, want prod", report.Name)
	}
	if report.Controller != "https://api.example.dev" {
		t.Errorf("controller = %q, want https://api.example.dev", report.Controller)
	}
	if strings.Contains(out, "swu_secret") {
		t.Errorf("token leaked into output:\n%s", out)
	}
}

func TestProfileCmd_RejectsPositionalArg(t *testing.T) {
	setProfileCmdFixture(t, "profiles:\n  prod: { controller: { url: https://x } }\n")
	err := runProfilesArgs([]string{"show", "prod"})
	if err == nil {
		t.Fatal("expected error on positional arg")
	}
	if !strings.Contains(err.Error(), "unexpected positional") {
		t.Errorf("message = %q", err.Error())
	}
}

func TestControllerURLDiagnosticNamesInputAndRepair(t *testing.T) {
	_, err := normalizeControllerURL("staging.sparkwing.dev")
	if err == nil || !strings.Contains(err.Error(), "staging.sparkwing.dev") || !strings.Contains(err.Error(), "https://") || !strings.Contains(err.Error(), "--controller") {
		t.Fatalf("URL refusal lacks repair: %v", err)
	}
}

func TestProfileDefaultSwitchControlsCLIAndKeepsLocalHelpersLocal(t *testing.T) {
	firstCalls, secondCalls := 0, 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls++
		json.NewEncoder(w).Encode(controllerwire.RunPage{Runs: []controllerwire.RunDetail{}})
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		json.NewEncoder(w).Encode(controllerwire.RunPage{Runs: []controllerwire.RunDetail{}})
	}))
	defer second.Close()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	setProfileCmdFixture(t, fmt.Sprintf("profiles:\n  first: {controller: {url: %s}}\n  second: {controller: {url: %s}}\n", first.URL, second.URL))
	for _, args := range [][]string{
		{"profiles", "set", "--profile", "first", "--default"},
		{"runs", "list"},
		{"profiles", "set", "--profile", "second", "--default"},
		{"runs", "list"},
		{"runs", "list", "--profile", "first"},
	} {
		var commandErr error
		captureStdout(t, func() { commandErr = runSparkwing(args) })
		if commandErr != nil {
			t.Fatalf("%v: %v", args, commandErr)
		}
	}
	if firstCalls != 2 || secondCalls != 1 {
		t.Fatalf("profile selection calls = %d, %d", firstCalls, secondCalls)
	}
	profiles, _, err := loadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(profiles)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"default":true`) {
		t.Fatalf("default not saved: %s", raw)
	}
	if profiles["first"].Default || !profiles["second"].Default {
		t.Fatalf("default did not switch: %s", raw)
	}
	for _, args := range [][]string{{"runs", "list", "--profile="}, {"run", "hello"}, {"schedules", "install", "--all-repos"}} {
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyConfiguredProfile(&inv); err != nil || inv.Value("profile") != "" {
			t.Fatalf("saved default changed a local invocation: %+v: %v", inv, err)
		}
	}

	local, err := resolveProfileFlag("")
	if err != nil || local != nil {
		t.Fatalf("local helper selected a remote default: %+v: %v", local, err)
	}
	captureStdout(t, func() {
		if err := runSparkwing([]string{"profiles", "set", "--profile", "second", "--default=false"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, args := range [][]string{{"runs", "list", "--profile="}, {"run", "hello"}, {"runs", "list"}} {
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyConfiguredProfile(&inv); err != nil || inv.Value("profile") != "" {
			t.Fatalf("local selection changed: %+v: %v", inv, err)
		}
	}
}

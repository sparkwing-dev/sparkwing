package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
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

func TestOnlyAnExplicitProfileReachesASavedController(t *testing.T) {
	calls := 0
	far := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(controllerwire.RunPage{Runs: []controllerwire.RunDetail{}})
	}))
	defer far.Close()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_PROFILE", "far")
	setProfileCmdFixture(t, fmt.Sprintf("profiles:\n  far: {controller: {url: %s}}\n", far.URL))
	captureStdout(t, func() {
		if err := runSparkwing([]string{"runs", "list", "--profile", "far"}); err != nil {
			t.Fatal(err)
		}
	})
	if calls == 0 {
		t.Fatal("--profile far did not reach the saved controller")
	}
	local, err := resolveProfileFlag("")
	if err != nil || local != nil {
		t.Fatalf("an empty --profile resolved a remote: %+v %v", local, err)
	}
}

func TestSavedDefaultProfileIsGone(t *testing.T) {
	setProfileCmdFixture(t, "profiles:\n  prod: {controller: {url: https://ci.example.com}}\n")
	if _, err := clisurface.Parse("sparkwing", []string{"profiles", "set", "--profile", "prod", "--default"}); err == nil {
		t.Fatal("profiles set accepted the removed --default")
	}
	setProfileCmdFixture(t, "profiles:\n  prod: {controller: {url: https://ci.example.com}, default: true}\n")
	err := runSparkwing([]string{"runs", "list", "-o", "json"})
	var field *userconfig.FieldError
	if !errors.As(err, &field) || field.Key != "profiles.prod.default" {
		t.Fatalf("a saved default was accepted: %v", err)
	}
}

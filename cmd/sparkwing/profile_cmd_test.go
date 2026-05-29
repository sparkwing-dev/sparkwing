package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setProfileCmdFixture(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("SPARKWING_PROFILES", path)
}

func TestProfileCmd_NoFlagUsesDefault(t *testing.T) {
	setProfileCmdFixture(t, `
default: prod
profiles:
  prod: { controller: { url: https://api.example.dev } }
  team: { state: { type: s3, bucket: team, prefix: state } }
`)
	out := captureStdout(t, func() {
		if err := runProfileCmd(nil); err != nil {
			t.Errorf("profile: %v", err)
		}
	})
	if !strings.Contains(out, "effective profile: prod") {
		t.Errorf("expected prod selected; got:\n%s", out)
	}
	if !strings.Contains(out, "default      prod ← selected") {
		t.Errorf("expected default-selected chain row; got:\n%s", out)
	}
}

func TestProfileCmd_FlagSelectsHypothetical(t *testing.T) {
	setProfileCmdFixture(t, `
default: prod
profiles:
  prod: { controller: { url: https://api.example.dev } }
  team: { state: { type: s3, bucket: team, prefix: state } }
`)
	out := captureStdout(t, func() {
		if err := runProfileCmd([]string{"--profile", "team"}); err != nil {
			t.Errorf("profile: %v", err)
		}
	})
	if !strings.Contains(out, "effective profile: team") {
		t.Errorf("expected team selected; got:\n%s", out)
	}
	if !strings.Contains(out, "flag (--profile team)") {
		t.Errorf("expected flag source; got:\n%s", out)
	}
}

func TestProfileCmd_NotFound(t *testing.T) {
	setProfileCmdFixture(t, `
profiles:
  prod: { controller: { url: https://api.example.dev } }
`)
	err := runProfileCmd([]string{"--profile", "bogus"})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), `profile "bogus" not found`) {
		t.Errorf("message = %q", err.Error())
	}
}

func TestProfileCmd_JSONEffectiveName(t *testing.T) {
	setProfileCmdFixture(t, `
default: prod
profiles:
  prod: { controller: { url: https://api.example.dev, token: swu_secret } }
`)
	out := captureStdout(t, func() {
		if err := runProfileCmd([]string{"-o", "json"}); err != nil {
			t.Errorf("profile: %v", err)
		}
	})
	var report profileReportJSON
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not parseable JSON: %v\n%s", err, out)
	}
	if report.Effective.Name != "prod" {
		t.Errorf("effective.name = %q, want prod", report.Effective.Name)
	}
	if report.Effective.Source != "default" {
		t.Errorf("effective.source = %q, want default", report.Effective.Source)
	}
	if report.Effective.State != "controller://prod" {
		t.Errorf("effective.state = %q, want controller://prod", report.Effective.State)
	}
	if len(report.Considered) != 4 {
		t.Errorf("expected 4 considered rows, got %d", len(report.Considered))
	}
	// Token must never appear in the output.
	if strings.Contains(out, "swu_secret") {
		t.Errorf("token leaked into output:\n%s", out)
	}
}

func TestProfileCmd_BuiltinLaptopFallback(t *testing.T) {
	// SPARKWING_PROFILES points at a path that doesn't exist -> empty
	// config, no default -> builtin laptop.
	t.Setenv("SPARKWING_PROFILES", filepath.Join(t.TempDir(), "absent.yaml"))
	out := captureStdout(t, func() {
		if err := runProfileCmd([]string{"-o", "json"}); err != nil {
			t.Errorf("profile: %v", err)
		}
	})
	var report profileReportJSON
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if report.Effective.Name != "laptop" {
		t.Errorf("name = %q, want laptop", report.Effective.Name)
	}
	if report.Effective.Source != "builtin" {
		t.Errorf("source = %q, want builtin", report.Effective.Source)
	}
	if report.Effective.State != "sqlite" {
		t.Errorf("state = %q, want sqlite (laptop default)", report.Effective.State)
	}
}

func TestProfileCmd_RejectsPositionalArg(t *testing.T) {
	setProfileCmdFixture(t, "profiles:\n  prod: { controller: { url: https://x } }\n")
	err := runProfileCmd([]string{"prod"})
	if err == nil {
		t.Fatal("expected error on positional arg")
	}
	if !strings.Contains(err.Error(), "takes no arguments") {
		t.Errorf("message = %q", err.Error())
	}
}

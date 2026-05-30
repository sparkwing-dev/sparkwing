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

func TestProfileCmd_NoFlagYieldsNoProfile(t *testing.T) {
	setProfileCmdFixture(t, `
profiles:
  prod: { controller: { url: https://api.example.dev } }
  team: { state: { type: s3, bucket: team, prefix: state } }
`)
	out := captureStdout(t, func() {
		if err := runProfileCmd(nil); err != nil {
			t.Errorf("profile: %v", err)
		}
	})
	if !strings.Contains(out, "no --profile") {
		t.Errorf("expected 'no --profile' banner; got:\n%s", out)
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
profiles:
  prod: { controller: { url: https://api.example.dev, token: swu_secret } }
`)
	out := captureStdout(t, func() {
		if err := runProfileCmd([]string{"--profile", "prod", "-o", "json"}); err != nil {
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
	if report.Effective.Source != "flag" {
		t.Errorf("effective.source = %q, want flag", report.Effective.Source)
	}
	if report.Effective.State != "controller://prod" {
		t.Errorf("effective.state = %q, want controller://prod", report.Effective.State)
	}
	// Token must never appear in the output.
	if strings.Contains(out, "swu_secret") {
		t.Errorf("token leaked into output:\n%s", out)
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

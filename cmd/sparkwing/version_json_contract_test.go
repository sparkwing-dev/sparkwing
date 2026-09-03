package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestVersionJSONKeepsTheFieldTheToolchainSwitchReads freezes the surface
// assertToolchainVersion depends on: the switch asks a freshly fetched release
// for its identity with `version -o json --offline` and reads cli.installed, so
// a release that drops either the flags or the field breaks every repo whose pin
// names it, in a place no other test looks.
func TestVersionJSONKeepsTheFieldTheToolchainSwitchReads(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runVersion([]string{"-o", "json", "--offline"}); err != nil {
			t.Fatalf("`sparkwing version -o json --offline`: %v", err)
		}
	})

	var generic map[string]any
	if err := json.Unmarshal([]byte(out), &generic); err != nil {
		t.Fatalf("version -o json is not an object: %v (%q)", err, out)
	}
	cli, ok := generic["cli"].(map[string]any)
	if !ok {
		t.Fatalf("version -o json lost its \"cli\" object: %q", out)
	}
	installed, ok := cli["installed"].(string)
	if !ok {
		t.Fatalf("version -o json lost \"cli\".\"installed\": %q", out)
	}
	if installed != installedVersion() {
		t.Errorf("cli.installed = %q, want the installed version %q", installed, installedVersion())
	}

	var report VersionReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("the switch's own decode failed: %v", err)
	}
	if report.CLI.Installed != installedVersion() {
		t.Errorf("decoded cli.installed = %q, want %q", report.CLI.Installed, installedVersion())
	}
	if strings.TrimSpace(out) == "" {
		t.Error("version -o json wrote nothing to stdout")
	}
}

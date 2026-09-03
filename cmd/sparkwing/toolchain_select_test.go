package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSparkwingModule(t *testing.T, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func pinModule(pin string) string {
	return "module example.com/pipes\n\ngo 1.26\n\nrequire " + sdkModulePath + " " + pin + "\n"
}

func withInstalledVersion(t *testing.T, v string) {
	t.Helper()
	prev := Version
	Version = v
	t.Cleanup(func() { Version = prev })
}

func TestPlanToolchainSwitch(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		pin       sdkPin
		mode      string
		active    string
		want      toolchainAction
	}{
		{"older cli switches", "v0.38.2", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainSwitch},
		{"equal stays", "v0.40.0", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainStay},
		{"newer cli stays", "v0.41.0", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainStay},
		{"replace stays", "v0.38.2", sdkPin{version: "v0.40.0", replace: ".."}, toolchainModeAuto, "", toolchainStay},
		{"pseudo pin stays", "v0.38.2", sdkPin{version: "v0.0.0-20260101120000-abcdef123456"}, toolchainModeAuto, "", toolchainStay},
		{"prerelease pin stays", "v0.38.2", sdkPin{version: "v0.40.0-rc1"}, toolchainModeAuto, "", toolchainStay},
		{"missing pin stays", "v0.38.2", sdkPin{}, toolchainModeAuto, "", toolchainStay},
		{"devel cli stays", "(devel)", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainStay},
		{"unknown cli stays", "(unknown)", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainStay},
		{"pseudo cli stays", "v0.0.0-20260101120000-abcdef123456", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainStay},
		{"dirty cli stays", "v0.38.2-dev+abc123", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainStay},
		{"local refuses", "v0.38.2", sdkPin{version: "v0.40.0"}, toolchainModeLocal, "", toolchainRefuse},
		{"local stays when current", "v0.41.0", sdkPin{version: "v0.40.0"}, toolchainModeLocal, "", toolchainStay},
		{"recursion guard stays", "v0.38.2", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "v0.40.0", toolchainStay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planToolchainSwitch(tc.installed, tc.pin, tc.mode, tc.active)
			if got.action != tc.want {
				t.Fatalf("action = %v, want %v", got.action, tc.want)
			}
		})
	}
}

func TestToolchainModeRejectsUnknownValue(t *testing.T) {
	for _, raw := range []string{"", "auto", "local"} {
		if _, err := toolchainMode(raw); err != nil {
			t.Fatalf("toolchainMode(%q) = %v, want no error", raw, err)
		}
	}
	_, err := toolchainMode("pinned")
	if err == nil {
		t.Fatal("toolchainMode(\"pinned\") accepted an unknown value")
	}
	msg := err.Error()
	for _, want := range []string{toolchainModeEnv, "pinned", toolchainModeAuto, toolchainModeLocal} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
	if code := exitCodeFor(err); code != 2 {
		t.Errorf("exit code = %d, want 2 (usage)", code)
	}
}

func TestReadSDKPin(t *testing.T) {
	dir := writeSparkwingModule(t, pinModule("v0.40.0"))
	pin, err := readSDKPin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pin.version != "v0.40.0" || pin.replace != "" {
		t.Fatalf("pin = %+v, want version v0.40.0 and no replace", pin)
	}

	replaced := writeSparkwingModule(t, pinModule("v0.40.0")+"\nreplace "+sdkModulePath+" => ..\n")
	pin, err = readSDKPin(replaced)
	if err != nil {
		t.Fatal(err)
	}
	if pin.replace != ".." {
		t.Fatalf("replace = %q, want ..", pin.replace)
	}
}

func TestSwitchToolchainLocalRefusalNamesTheVersion(t *testing.T) {
	t.Setenv(toolchainModeEnv, toolchainModeLocal)
	t.Setenv(toolchainActiveEnv, "")
	withInstalledVersion(t, "v0.38.2")

	err := switchToolchain(writeSparkwingModule(t, pinModule("v0.40.0")))
	if err == nil {
		t.Fatal("SPARKWING_TOOLCHAIN=local accepted a pin the installed CLI cannot serve")
	}
	msg := err.Error()
	for _, want := range []string{"v0.40.0", "v0.38.2", "SPARKWING_TOOLCHAIN=local", "sparkwing update --version v0.40.0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not contain %q", msg, want)
		}
	}
}

func TestSwitchToolchainStaysUnderARecursionGuard(t *testing.T) {
	t.Setenv(toolchainModeEnv, "")
	t.Setenv(toolchainActiveEnv, "v0.40.0")
	withInstalledVersion(t, "v0.38.2")

	if err := switchToolchain(writeSparkwingModule(t, pinModule("v0.40.0"))); err != nil {
		t.Fatalf("guarded child tried to switch again: %v", err)
	}
}

func TestEnsureToolchainBinaryFetchesVerifiesAndCaches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	priv := withTestUpdateKey(t)
	assetBytes := []byte("SPARKWING-TOOLCHAIN-v9.9.9\x00\x01")
	newReleaseServer(t, "v9.9.9", assetBytes, priv, releaseServerOpts{})

	var out bytes.Buffer
	binPath, err := ensureToolchainBinary(&out, "v9.9.9")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	want := filepath.Join(home, "toolchains", "v9.9.9", "sparkwing")
	if binPath != want {
		t.Fatalf("store path = %q, want %q", binPath, want)
	}
	body, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, assetBytes) {
		t.Fatal("stored binary does not match the verified release asset")
	}
	if !strings.Contains(out.String(), "fetched and verified sparkwing v9.9.9") {
		t.Fatalf("fetch notice missing from %q", out.String())
	}
	if !strings.Contains(out.String(), mustSHA256(assetBytes)) {
		t.Fatalf("fetch notice omits the verified digest: %q", out.String())
	}

	updateBaseURL = "http://127.0.0.1:1"
	out.Reset()
	if _, err := ensureToolchainBinary(&out, "v9.9.9"); err != nil {
		t.Fatalf("cache hit reached the network: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("cache hit printed %q, want nothing", out.String())
	}
}

func TestEnsureToolchainBinaryRefetchesOnDigestMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	priv := withTestUpdateKey(t)
	assetBytes := []byte("SPARKWING-TOOLCHAIN-v9.9.9\x00\x01")
	newReleaseServer(t, "v9.9.9", assetBytes, priv, releaseServerOpts{})

	binPath, err := ensureToolchainBinary(&bytes.Buffer{}, "v9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("TAMPERED"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if _, err := ensureToolchainBinary(&out, "v9.9.9"); err != nil {
		t.Fatalf("re-fetch after tampering: %v", err)
	}
	body, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, assetBytes) {
		t.Fatal("a tampered cached binary survived instead of being replaced")
	}
	if !strings.Contains(out.String(), "fetched and verified") {
		t.Fatalf("tampered cache did not re-fetch: %q", out.String())
	}
}

func TestToolchainFetchErrorNamesVersionURLAndRemedy(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	withTestUpdateKey(t)
	prev := updateBaseURL
	updateBaseURL = "http://127.0.0.1:1"
	t.Cleanup(func() { updateBaseURL = prev })

	_, err := ensureToolchainBinary(&bytes.Buffer{}, "v9.9.9")
	if err == nil {
		t.Fatal("an unreachable release host produced no error")
	}
	msg := err.Error()
	for _, want := range []string{"v9.9.9", "http://127.0.0.1:1/v9.9.9", "sparkwing update --version v9.9.9"} {
		if !strings.Contains(msg, want) {
			t.Errorf("fetch error %q does not contain %q", msg, want)
		}
	}
}

func TestInfoSDKPinReportsBothVersions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv(toolchainModeEnv, "")
	t.Setenv(toolchainActiveEnv, "")

	info := Info{
		Version: parseInfoVersion("v0.38.2"),
		Binary:  "/usr/local/bin/sparkwing",
		Project: InfoProject{Found: true, SparkwingDir: writeSparkwingModule(t, pinModule("v0.40.0"))},
	}
	pin := gatherSDKPin(info)
	if pin == nil {
		t.Fatal("info reported no SDK pin while the pin and the CLI differ")
	}
	if !pin.Switches || pin.Runs != "v0.40.0" {
		t.Fatalf("pin = %+v, want a switch to v0.40.0", pin)
	}
	if pin.RunsFrom != filepath.Join(home, "toolchains", "v0.40.0", "sparkwing") {
		t.Fatalf("runs_from = %q", pin.RunsFrom)
	}
	line := sdkPinLine(pin)
	for _, want := range []string{"v0.40.0", "switches"} {
		if !strings.Contains(line, want) {
			t.Errorf("info line %q does not contain %q", line, want)
		}
	}

	info.Version = parseInfoVersion("v0.41.0")
	pin = gatherSDKPin(info)
	if pin == nil || pin.Switches {
		t.Fatalf("a newer CLI should run the pin itself, got %+v", pin)
	}
	if !strings.Contains(sdkPinLine(pin), "v0.41.0") {
		t.Errorf("info line %q does not name the installed CLI", sdkPinLine(pin))
	}

	info.Version = parseInfoVersion("v0.40.0")
	if pin := gatherSDKPin(info); pin != nil {
		t.Fatalf("matching versions still reported a pin line: %+v", pin)
	}
}

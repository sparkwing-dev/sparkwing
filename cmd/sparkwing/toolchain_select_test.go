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
	return writeSparkwingModuleIn(t, t.TempDir(), body)
}

func writeSparkwingModuleIn(t *testing.T, repo, body string) string {
	t.Helper()
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
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

func withToolchainActive(t *testing.T, v string) {
	t.Helper()
	t.Setenv(toolchainActiveEnv, v)
	prev := toolchainActive
	toolchainActive = takeToolchainActive()
	t.Cleanup(func() { toolchainActive = prev })
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
		{"incompatible pin stays", "v0.38.2", sdkPin{version: "v2.0.0+incompatible"}, toolchainModeAuto, "", toolchainStay},
		{"missing pin stays", "v0.38.2", sdkPin{}, toolchainModeAuto, "", toolchainStay},
		{"devel cli stays", "(devel)", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainStay},
		{"unknown cli stays", "(unknown)", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainStay},
		{"pseudo cli stays", "v0.0.0-20260101120000-abcdef123456", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainStay},
		{"dirty cli stays", "v0.38.2-dev+abc123", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "", toolchainStay},
		{"local refuses", "v0.38.2", sdkPin{version: "v0.40.0"}, toolchainModeLocal, "", toolchainRefuse},
		{"local stays when current", "v0.41.0", sdkPin{version: "v0.40.0"}, toolchainModeLocal, "", toolchainStay},
		{"guard for this pin stays", "v0.38.2", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "v0.40.0", toolchainStay},
		{"guard for another pin still switches", "v0.38.2", sdkPin{version: "v0.40.0"}, toolchainModeAuto, "v0.39.0", toolchainSwitch},
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

func TestSwitchToolchainStaysOnAnUnreadableModule(t *testing.T) {
	withToolchainActive(t, "")
	withInstalledVersion(t, "v0.38.2")
	dir := writeSparkwingModule(t, "module example.com/pipes\n\nthis is not a go.mod\n")
	if err := switchToolchain(dir); err != nil {
		t.Fatalf("an unparsable go.mod should defer to the compiler, got %v", err)
	}
}

func TestSwitchToolchainLocalRefusalNamesTheVersion(t *testing.T) {
	t.Setenv(toolchainModeEnv, toolchainModeLocal)
	withToolchainActive(t, "")
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
	withToolchainActive(t, "v0.40.0")
	withInstalledVersion(t, "v0.40.0")

	if err := switchToolchain(writeSparkwingModule(t, pinModule("v0.40.0"))); err != nil {
		t.Fatalf("guarded child tried to switch again: %v", err)
	}
}

func TestTakeToolchainActiveClearsTheEnvironmentItRead(t *testing.T) {
	withToolchainActive(t, "v0.40.0")
	if toolchainActive != "v0.40.0" {
		t.Fatalf("toolchainActive = %q, want v0.40.0", toolchainActive)
	}
	if got := os.Getenv(toolchainActiveEnv); got != "" {
		t.Fatalf("%s survived as %q, so the pipeline binary and the daemon would inherit it", toolchainActiveEnv, got)
	}
}

func TestSwitchToolchainRefusesAMislabelledRelease(t *testing.T) {
	withToolchainActive(t, "v0.40.0")
	withInstalledVersion(t, "v0.39.0")

	err := switchToolchain(writeSparkwingModule(t, pinModule("v0.40.0")))
	if err == nil {
		t.Fatal("a child that is not the version it was switched to kept running")
	}
	for _, want := range []string{"v0.40.0", "v0.39.0", "toolchains/v0.40.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}

func TestSwitchToolchainIgnoresAHandSetGuardOnASourceBuild(t *testing.T) {
	t.Setenv(toolchainModeEnv, "")
	withToolchainActive(t, "v0.40.0")
	withInstalledVersion(t, "(devel)")

	if err := switchToolchain(writeSparkwingModule(t, pinModule("v0.41.0"))); err != nil {
		t.Fatalf("a source build with an exported guard should decide normally, got %v", err)
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
	withToolchainActive(t, "")

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

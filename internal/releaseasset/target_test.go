package releaseasset

import "testing"

func TestNamesContainEveryOfficialRunnerPlatform(t *testing.T) {
	want := map[string]bool{
		"sparkwing-runner-darwin-amd64":      true,
		"sparkwing-runner-darwin-arm64":      true,
		"sparkwing-runner-linux-amd64":       true,
		"sparkwing-runner-linux-arm64":       true,
		"sparkwing-runner-windows-amd64.exe": true,
		"sparkwing-runner-windows-arm64.exe": true,
	}
	for _, name := range Names() {
		delete(want, name)
	}
	for name := range want {
		t.Errorf("Names() does not contain %s", name)
	}
}

func TestParseNameRoundTripsTheClosedAssetSet(t *testing.T) {
	for _, name := range Names() {
		target, err := ParseName(name)
		if err != nil {
			t.Fatalf("ParseName(%q): %v", name, err)
		}
		got, err := target.Name()
		if err != nil || got != name {
			t.Fatalf("Target.Name() = %q, %v; want %q", got, err, name)
		}
	}
	if _, err := ParseName("sparkwing-runner-linux-amd64.sig"); err == nil {
		t.Error("ParseName accepted a signature sidecar as an executable")
	}
}

func TestTargetNameRejectsNonReleaseCombinations(t *testing.T) {
	for _, target := range []Target{
		{Binary: "sparkwing-fake", GOOS: "linux", GOARCH: "amd64"},
		{Binary: SparkwingCache, GOOS: "windows", GOARCH: "amd64"},
		{Binary: SparkwingRunner, GOOS: "plan9", GOARCH: "amd64"},
		{Binary: SparkwingRunner, GOOS: "windows", GOARCH: "386"},
	} {
		if name, err := target.Name(); err == nil {
			t.Errorf("Target(%+v).Name() = %q", target, name)
		}
	}
}

package toolchain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectKeepsStableReleaseMatrix(t *testing.T) {
	tests := []struct {
		name      string
		selection Selection
		want      Action
	}{
		{"older switches", Selection{Installed: "v0.38.2", Required: "v0.40.0", Mode: ModeAuto}, Switch},
		{"equal stays", Selection{Installed: "v0.40.0", Required: "v0.40.0", Mode: ModeAuto}, Stay},
		{"newer stays", Selection{Installed: "v0.41.0", Required: "v0.40.0", Mode: ModeAuto}, Stay},
		{"replacement stays", Selection{Installed: "v0.38.2", Required: "v0.40.0", Replacement: "..", Mode: ModeAuto}, Stay},
		{"pseudo requirement stays", Selection{Installed: "v0.38.2", Required: "v0.0.0-20260101120000-abcdef123456", Mode: ModeAuto}, Stay},
		{"source build stays", Selection{Installed: "(devel)", Required: "v0.40.0", Mode: ModeAuto}, Stay},
		{"local refuses", Selection{Installed: "v0.38.2", Required: "v0.40.0", Mode: ModeLocal}, Refuse},
		{"matching guard stays", Selection{Installed: "v0.38.2", Required: "v0.40.0", Active: "v0.40.0", Mode: ModeAuto}, Stay},
		{"different guard still switches", Selection{Installed: "v0.38.2", Required: "v0.40.0", Active: "v0.39.0", Mode: ModeAuto}, Switch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Select(test.selection); got != test.want {
				t.Fatalf("Select(%+v) = %v, want %v", test.selection, got, test.want)
			}
		})
	}
}

func TestParseMode(t *testing.T) {
	for raw, want := range map[string]Mode{"": ModeAuto, " auto ": ModeAuto, "local": ModeLocal} {
		got, err := ParseMode(raw)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := ParseMode("pinned"); err == nil {
		t.Error("ParseMode accepted an unknown mode")
	}
}

func TestResolveHoldPrefersEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-hold")
	if err := os.WriteFile(path, []byte("v0.15\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveHold(Hold{Source: "SPARKWING_TEST_VERSION_HOLD"}, path); got.Value != "v0.15" || got.Source != path {
		t.Fatalf("file hold = %+v", got)
	}
	if got := ResolveHold(Hold{Value: "v0.10", Source: "SPARKWING_TEST_VERSION_HOLD"}, path); got.Value != "v0.10" || got.Source != "SPARKWING_TEST_VERSION_HOLD" {
		t.Fatalf("environment hold = %+v", got)
	}
}

func TestResolveHoldStrictDistinguishesInvalidFromAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-hold")
	environment := Hold{Source: "SPARKWING_TEST_VERSION_HOLD"}
	if hold, err := ResolveHoldStrict(environment, path); err != nil || hold.Value != "" {
		t.Fatalf("absent hold = %+v, %v", hold, err)
	}
	if err := os.WriteFile(path, []byte("latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hold, err := ResolveHoldStrict(environment, path); err == nil || hold.Source != path {
		t.Fatalf("invalid file hold = %+v, %v", hold, err)
	}
	environment.Value = "v0.15-rc.1"
	if hold, err := ResolveHoldStrict(environment, path); err == nil || hold.Source != "SPARKWING_TEST_VERSION_HOLD" {
		t.Fatalf("invalid environment hold = %+v, %v", hold, err)
	}
	for _, value := range []string{"v0.15", "v0.15.4"} {
		environment.Value = value
		if hold, err := ResolveHoldStrict(environment, path); err != nil || hold.Value != value {
			t.Errorf("valid hold %q = %+v, %v", value, hold, err)
		}
	}
}

func TestValidHoldRejectsValuesThatWouldDisableTheCeiling(t *testing.T) {
	for _, value := range []string{"", "latest", "0.15", "v0", "v0.15.0-rc.1", "v0.15.0+build", "v01.15"} {
		if ValidHold(value) {
			t.Errorf("ValidHold(%q) = true", value)
		}
	}
}

func TestExceedsHold(t *testing.T) {
	tests := []struct {
		target string
		hold   string
		want   bool
	}{
		{"v0.16.0", "v0.15", true},
		{"v0.15.9", "v0.15", false},
		{"v0.15.5", "v0.15.4", true},
		{"v0.15.4", "v0.15.4", false},
		{"latest", "v0.15", false},
	}
	for _, test := range tests {
		if got := ExceedsHold(test.target, test.hold); got != test.want {
			t.Errorf("ExceedsHold(%q, %q) = %v, want %v", test.target, test.hold, got, test.want)
		}
	}
}

package toolchain

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestResolveHoldReadsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-hold")
	if err := os.WriteFile(path, []byte("v0.15\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveHold(path); err != nil || got.Value != "v0.15" || got.Source != path {
		t.Fatalf("file hold = %+v, %v", got, err)
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

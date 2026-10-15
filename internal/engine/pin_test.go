package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPinRequiresExactRelease(t *testing.T) {
	for _, tc := range []struct {
		name, goMod, version string
		bad                  bool
	}{
		{"older exact", "module fixture\nrequire github.com/sparkwing-dev/sparkwing v0.10.2\n", "v0.10.2", false},
		{"prerelease", "module fixture\nrequire github.com/sparkwing-dev/sparkwing v0.10.2-rc.1\n", "v0.10.2-rc.1", false},
		{"missing", "module fixture\n", "", true},
		{"replacement", "module fixture\nrequire github.com/sparkwing-dev/sparkwing v0.10.2\nreplace github.com/sparkwing-dev/sparkwing => ../sdk\n", "", true},
		{"pseudo", "module fixture\nrequire github.com/sparkwing-dev/sparkwing v0.0.0-20261014120000-abcdefabcdef\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "go.mod", []byte(tc.goMod))
			pin, err := ReadPin(dir, GoSDK)
			if (err != nil) != tc.bad {
				t.Fatalf("pin=%+v error=%v", pin, err)
			}
			if !tc.bad && (pin.Version != tc.version || pin.InputsDigest == "") {
				t.Fatalf("pin=%+v", pin)
			}
		})
	}
}

func TestReadTypeScriptPinRejectsConflictAndRangeWithoutLock(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "package.json", []byte(`{"dependencies":{"@sparkwing/sdk":"^0.10.0"}}`))
	if _, err := ReadPin(dir, TypeScriptSDK); err == nil {
		t.Fatal("range without lock accepted")
	}
	writeFixture(t, dir, "package-lock.json", []byte(`{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"^0.10.0"}},"node_modules/@sparkwing/sdk":{"version":"0.10.2"}}}`))
	pin, err := ReadPin(dir, TypeScriptSDK)
	if err != nil {
		t.Fatal(err)
	}
	version, err := VersionForPin(pin)
	if err != nil || version != "v0.10.2" {
		t.Fatalf("%s %v", version, err)
	}
	writeFixture(t, dir, "package.json", []byte(`{"dependencies":{"@sparkwing/sdk":"0.11.0"}}`))
	if _, err := ReadPin(dir, TypeScriptSDK); err == nil {
		t.Fatal("conflicting manifest accepted")
	}
}

func writeFixture(t *testing.T, dir, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNpmRangesKeepZeroMajorAndPrereleaseBoundaries(t *testing.T) {
	for _, tc := range []struct {
		rangeSpec, version string
		want               bool
	}{
		{"^0.0.1", "0.0.1", true}, {"^0.0.1", "0.0.2", false}, {"^0.1.0", "0.2.0", false},
		{"^1.2.3", "1.9.0", true}, {"^1.2.3", "2.0.0", false}, {"~1.2.3", "1.3.0", false},
		{"^1.2.3", "1.3.0-rc.1", false}, {"^1.2.3-rc.1", "1.2.3-rc.2", true}, {"^1.2.3-rc.1", "1.3.0-rc.2", false},
		{">=1.2.3 <2", "1.5.0", true}, {">=1.2.3 <2", "2.0.0", false}, {"1.2.x || >=2.0.0", "2.1.0", true},
		{"1.2 - 2.3", "2.3.9", true}, {"1.2 - 2.3", "2.4.0", false}, {"^0.0", "0.0.9", true},
	} {
		t.Run(tc.rangeSpec+"_"+tc.version, func(t *testing.T) {
			if got := npmSatisfies(tc.rangeSpec, tc.version); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

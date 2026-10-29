package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestReadTypeScriptLocalPin(t *testing.T) {
	for _, lockVersion := range []int{1, 2, 3} {
		for _, spec := range []string{"file:sdk.tgz", "./sdk.tgz", "sdk.tgz", "file:sdk"} {
			t.Run(fmt.Sprintf("v%d/%s", lockVersion, spec), func(t *testing.T) {
				dir := t.TempDir()
				localSDK(t, dir, "0.68.0-qa.16")
				resolved := "file:sdk.tgz"
				entry := map[string]any{"version": "0.68.0-qa.16", "resolved": resolved}
				packages := map[string]any{"": map[string]any{"dependencies": map[string]string{typeScriptPackage: spec}}, "node_modules/" + typeScriptPackage: entry}
				if spec == "file:sdk" {
					resolved = "file:sdk"
					entry = map[string]any{"resolved": "sdk", "link": true}
					packages["node_modules/"+typeScriptPackage] = entry
					packages["sdk"] = map[string]string{"version": "0.68.0-qa.16"}
				}
				writeJSONFixture(t, dir, "package.json", map[string]any{"dependencies": map[string]string{typeScriptPackage: spec}})
				writeJSONFixture(t, dir, "package-lock.json", map[string]any{"lockfileVersion": lockVersion, "packages": packages,
					"dependencies": map[string]any{typeScriptPackage: map[string]string{"version": spec, "resolved": resolved}}})
				pin, err := ReadPin(dir, engineid.TypeScriptSDK)
				if err != nil {
					t.Fatal(err)
				}
				version, err := VersionForPin(pin)
				if err != nil || version != "v0.68.0-qa.16" {
					t.Fatalf("version=%s error=%v", version, err)
				}
				input := "sdk.tgz"
				if spec == "file:sdk" {
					input = "sdk/package.json"
				}
				body, err := os.ReadFile(filepath.Join(dir, input))
				if err != nil {
					t.Fatal(err)
				}
				if input == "sdk.tgz" {
					body[4]++
				} else {
					body = append(body, '\n')
				}
				writeFixture(t, dir, input, body)
				changed, err := ReadPin(dir, engineid.TypeScriptSDK)
				if err != nil || changed.InputsDigest == pin.InputsDigest {
					t.Fatalf("changed package was not bound to pin: %v", err)
				}
				localSDK(t, dir, "0.68.0-qa.17")
				if _, err := ReadPin(dir, engineid.TypeScriptSDK); lockVersion != 1 && err == nil {
					t.Fatal("source/lock version mismatch accepted")
				}
			})
		}
	}
}

func localSDK(t *testing.T, dir, version string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"name": typeScriptPackage, "version": version})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sdk"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, dir, "sdk/package.json", body)
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o600, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, dir, "sdk.tgz", archive.Bytes())
}

func writeJSONFixture(t *testing.T, dir, name string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, dir, name, body)
}

func TestReadTypeScriptLocalPinRejectsConflictingSources(t *testing.T) {
	for _, tc := range []struct{ source, legacySource, legacyVersion string }{
		{"file:other.tgz", "", "0.68.0"},
		{"file:sdk.tgz", "file:other.tgz", "0.68.0"},
		{"file:sdk.tgz", "file:sdk.tgz", "0.69.0"},
		{"https://example.com/sdk.tgz", "", "0.68.0"},
	} {
		t.Run(tc.source+"/"+tc.legacySource+"/"+tc.legacyVersion, func(t *testing.T) {
			dir := t.TempDir()
			localSDK(t, dir, "0.68.0")
			writeFixture(t, dir, "package.json", []byte(`{"dependencies":{"@sparkwing/sdk":"file:sdk.tgz"}}`))
			writeJSONFixture(t, dir, "package-lock.json", map[string]any{"lockfileVersion": 2,
				"packages":     map[string]any{"": map[string]any{"dependencies": map[string]string{typeScriptPackage: "file:sdk.tgz"}}, "node_modules/" + typeScriptPackage: map[string]string{"version": "0.68.0", "resolved": tc.source}},
				"dependencies": map[string]any{typeScriptPackage: map[string]string{"version": tc.legacyVersion, "resolved": tc.legacySource}},
			})
			if _, err := ReadPin(dir, engineid.TypeScriptSDK); err == nil {
				t.Fatal("conflicting local source accepted")
			}
		})
	}
}

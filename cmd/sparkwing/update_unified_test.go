package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func isolateUpdateTests(t *testing.T) {
	t.Helper()
	fetch, release, revision, installed, download, api := updateFetchLatest, updateLookupRelease, updateLookupRevision, updateReadInstalled, updateDownloadInstall, updateReleaseAPI
	t.Cleanup(func() {
		updateFetchLatest = fetch
		updateLookupRelease = release
		updateLookupRevision = revision
		updateReadInstalled = installed
		updateDownloadInstall = download
		updateReleaseAPI = api
	})
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SPARKWING_HOME", filepath.Join(home, "state"))
	t.Setenv(versionHoldEnv, "")
	updateFetchLatest = func() (string, error) { return "v0.49.0", nil }
	updateLookupRelease = lookupUpdateRelease
	updateLookupRevision = lookupUpdateRevision
	updateDownloadInstall = func(string, string) (installedRelease, error) {
		t.Error("check attempted an install")
		return installedRelease{}, errors.New("unexpected install")
	}
}

func updateMetadataFixture(t *testing.T) (*atomic.Int32, string) {
	t.Helper()
	var requests atomic.Int32
	revision := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if strings.HasPrefix(r.URL.Path, "/releases/tags/") {
			tag := strings.TrimPrefix(r.URL.Path, "/releases/tags/")
			if tag == "v9.99.99" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"tag_name":%q,"draft":false}`, tag)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/git/ref/tags/") {
			fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, revision)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	updateReleaseAPI = server.URL
	return &requests, revision
}

func checkUpdate(t *testing.T, args ...string) (updateCheckReport, int) {
	t.Helper()
	var commandError error
	out := captureStdout(t, func() { commandError = runSparkwing(append([]string{"update"}, args...)) })
	var report updateCheckReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("update check output is not one JSON record: %v %q (%v)", err, out, commandError)
	}
	if report.Kind != "update_check" || report.Tool != "sparkwing" || report.Strategy != "release" {
		t.Fatalf("shared update contract: %+v", report)
	}
	return report, exitCodeFor(commandError)
}

func TestUnifiedUpdateCheckReleaseStatesAndProvenance(t *testing.T) {
	isolateUpdateTests(t)
	_, revision := updateMetadataFixture(t)
	clean, dirty := false, true
	for _, tc := range []struct {
		name, current, target string
		dirty                 *bool
		revision, status      string
		code                  int
	}{
		{"current", "v0.49.0", "", &clean, revision, "current", 0},
		{"behind", "v0.48.0", "", &clean, revision, "update_available", 1},
		{"ahead", "v0.50.0", "", &clean, revision, "ahead", 0},
		{"explicit target", "v0.49.0", "v0.50.0", &clean, revision, "update_available", 1},
		{"missing requested release", "v0.49.0", "v9.99.99", &clean, revision, "unknown", 2},
		{"dirty same label", "v0.49.0", "", &dirty, revision, "unknown", 2},
		{"different commit same label", "v0.49.0", "", &clean, strings.Repeat("b", 40), "unknown", 2},
		{"different commit older label", "v0.48.0", "", &clean, strings.Repeat("b", 40), "unknown", 2},
		{"missing provenance", "v0.49.0", "", nil, "", "unknown", 2},
		{"development", "v0.49.0-dev+abcdef12", "", &clean, revision, "unknown", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updateReadInstalled = func() updateIdentity {
				return updateIdentity{Version: tc.current, Revision: tc.revision, Dirty: tc.dirty}
			}
			args := []string{"--check"}
			if tc.target != "" {
				args = append(args, "--version", tc.target)
			}
			report, code := checkUpdate(t, args...)
			if report.Target != "cli" || report.Status != tc.status || code != tc.code {
				t.Fatalf("got %+v exit%d", report, code)
			}
		})
	}
	for _, path := range []string{os.Getenv("SPARKWING_HOME"), os.Getenv("XDG_CONFIG_HOME")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("check wrote %s", path)
		}
	}
}

func TestUnifiedUpdateRejectsInputsBeforeProbes(t *testing.T) {
	isolateUpdateTests(t)
	called := false
	updateFetchLatest = func() (string, error) { called = true; return "v0.49.0", nil }
	updateReadInstalled = func() updateIdentity { called = true; return updateIdentity{} }
	updateLookupRelease = func(string) error { called = true; return nil }
	for _, args := range [][]string{{"--cli", "--sdk"}, {"--sdk", "--force"}, {"--sdk", "--force=false"}, {"--sdk", "--override-hold"}, {"--sdk=false"}, {"--version", "../../not-a-tag"}, {"--version="}, {"--check", "-o", "jsonl"}, {"--output="}, {"extra"}} {
		if err := runSparkwing(append([]string{"update"}, args...)); err == nil {
			t.Errorf("invalid args accepted: %v", args)
		}
	}
	if called {
		t.Fatal("invalid input performed a probe")
	}
	for _, path := range []string{os.Getenv("SPARKWING_HOME"), os.Getenv("XDG_CONFIG_HOME")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("invalid args wrote %s", path)
		}
	}
}

func TestUnifiedUpdateCheckPreservesHoldAndOutputModes(t *testing.T) {
	isolateUpdateTests(t)
	_, revision := updateMetadataFixture(t)
	clean := false
	updateReadInstalled = func() updateIdentity { return updateIdentity{Version: "v0.48.0", Revision: revision, Dirty: &clean} }
	t.Setenv(versionHoldEnv, "v0.48")
	report, code := checkUpdate(t, "--check", "--cli")
	if code != 1 || report.BlockedReason == "" {
		t.Fatalf("hold not reported: %+v %d", report, code)
	}
	if os.Getenv(versionHoldEnv) != "v0.48" {
		t.Fatal("check changed hold")
	}
	for _, mode := range []string{"plain", "pretty"} {
		var err error
		out := captureStdout(t, func() { err = runUpdate([]string{"--check", "-o", mode}) })
		if exitCodeFor(err) != 1 {
			t.Fatalf("mode %s exit: %v", mode, err)
		}
		if mode == "plain" && out != "update_available\n" {
			t.Fatalf("plain output: %q", out)
		}
		if mode == "pretty" && (!strings.Contains(out, "installed:") || !strings.Contains(out, "blocked:")) {
			t.Fatalf("pretty output: %q", out)
		}
	}
}

func sdkUpdateFixture(t *testing.T, extra string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main(){}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	module := "module fixture\n\ngo 1.26.0\n\nrequire " + sdkModulePath + " v0.48.0\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return dir
}

func TestUnifiedSDKCheckReadsPinWithoutGoOrModuleWrites(t *testing.T) {
	isolateUpdateTests(t)
	updateMetadataFixture(t)
	dir := sdkUpdateFixture(t, "")
	before, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	spy := filepath.Join(t.TempDir(), "go-called")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\ntouch '"+spy+"'\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	report, code := checkUpdate(t, "--sdk", "--check", "--version", "v0.49.0")
	if report.Target != "sdk" || report.Status != "update_available" || code != 1 || report.Installed.Version != "v0.48.0" {
		t.Fatalf("SDK report: %+v code%d", report, code)
	}
	after, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil || string(after) != string(before) {
		t.Fatal("SDK check changed go.mod")
	}
	if _, err := os.Stat(spy); !os.IsNotExist(err) {
		t.Fatal("SDK check invoked Go")
	}
	if _, err := os.Stat(filepath.Join(dir, "go.sum")); !os.IsNotExist(err) {
		t.Fatal("SDK check wrote go.sum")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), append(before, []byte("replace "+sdkModulePath+" => ../local-sdk\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	report, code = checkUpdate(t, "--sdk", "--check")
	if report.Status != "unknown" || code != 2 || !strings.Contains(report.Reason, "replacement") {
		t.Fatalf("replacement misreported: %+v code%d", report, code)
	}
}

func TestUnifiedUpdateReceiptUsesInstalledArtifactAndReinstallsLocalSameLabel(t *testing.T) {
	isolateUpdateTests(t)
	_, revision := updateMetadataFixture(t)
	clean := false
	updateReadInstalled = func() updateIdentity {
		return updateIdentity{Version: "v0.49.0", Revision: strings.Repeat("b", 40), Dirty: &clean}
	}
	path := filepath.Join(t.TempDir(), "installed-candidate")
	called := false
	updateDownloadInstall = func(version, _ string) (installedRelease, error) {
		called = true
		if err := os.WriteFile(path, []byte("verified fixture"), 0o700); err != nil {
			return installedRelease{}, err
		}
		return installedRelease{path: path, version: version, digest: "fixture-sha256"}, nil
	}
	var commandError error
	out := captureStdout(t, func() { commandError = runUpdate([]string{"--cli", "--version", "v0.49.0"}) })
	if commandError != nil {
		t.Fatal(commandError)
	}
	var result updateReceipt
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("receipt polluted by progress: %v %q", err, out)
	}
	if !called || result.Kind != "update" || result.Status != "updated" || result.After.Version != "v0.49.0" || result.After.Path != path || result.After.Revision != "" {
		t.Fatalf("false update receipt: %+v", result)
	}
	updateReadInstalled = func() updateIdentity { return updateIdentity{Version: "v0.49.0", Revision: revision, Dirty: &clean} }
	called = false
	out = captureStdout(t, func() { commandError = runUpdate([]string{"--version", "v0.49.0"}) })
	if commandError != nil {
		t.Fatal(commandError)
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if called || result.Status != "current" {
		t.Fatal("verified current release was reinstalled")
	}
}

func TestVersionUpdateIsAbsentFromDispatchAndRegistry(t *testing.T) {
	isolateUpdateTests(t)
	for _, command := range allCommands {
		if command.Path == "sparkwing version update" {
			t.Fatal("retired update command remains registered")
		}
	}
	if err := runVersion([]string{"update"}); err == nil {
		t.Fatal("retired version update was accepted")
	}
}

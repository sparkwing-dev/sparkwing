package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func isolateUpdateTests(t *testing.T) {
	t.Helper()
	fetch, release, revision, installed, download, api := updateFetchLatest, updateLookupRelease, updateLookupRevision, updateReadInstalled, updateDownloadInstall, updateReleaseAPI
	invoking, destination := updateReadInvoking, updateDestination
	t.Cleanup(func() {
		updateFetchLatest = fetch
		updateLookupRelease = release
		updateLookupRevision = revision
		updateReadInstalled = installed
		updateDownloadInstall = download
		updateReleaseAPI = api
		updateReadInvoking = invoking
		updateDestination = destination
	})
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "installed"), []byte("fixture before"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SPARKWING_HOME", filepath.Join(home, "state"))
	t.Setenv(versionHoldEnv, "")
	updateFetchLatest = func(context.Context) (string, error) { return "v0.49.0", nil }
	updateLookupRelease = lookupUpdateRelease
	updateLookupRevision = lookupUpdateRevision
	updateReadInvoking = func() updateIdentity { return updateReadInstalled() }
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
	updateFetchLatest = func(context.Context) (string, error) { called = true; return "v0.49.0", nil }
	updateReadInstalled = func() updateIdentity { called = true; return updateIdentity{} }
	updateLookupRelease = func(context.Context, string) error { called = true; return nil }
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
	updateReadInstalled = func() updateIdentity {
		return updateIdentity{Version: "v0.48.0", Revision: revision, Dirty: &clean, Path: filepath.Join(os.Getenv("HOME"), "installed")}
	}
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

func TestUnifiedSameLabelLocalBuildStillReportsOperatorHold(t *testing.T) {
	isolateUpdateTests(t)
	updateMetadataFixture(t)
	clean := false
	updateReadInstalled = func() updateIdentity {
		return updateIdentity{Version: "v0.49.0", Revision: strings.Repeat("b", 40), Dirty: &clean, Path: filepath.Join(os.Getenv("HOME"), "installed")}
	}
	t.Setenv(versionHoldEnv, "v0.48")
	report, code := checkUpdate(t, "--check", "--version", "v0.49.0")
	if code != 2 || report.Status != "unknown" || !strings.Contains(report.BlockedReason, "operator CLI version hold") {
		t.Fatalf("same-label local build hid hold: %+v %d", report, code)
	}
	if _, err := updateBinary("v0.49.0", false, false); err == nil || !strings.Contains(err.Error(), "operator set a version hold") {
		t.Fatalf("actual local replacement ignored hold: %v", err)
	}
}

func TestUnifiedUpdateRefusesUnreadableHoldEvenForCurrentVersion(t *testing.T) {
	isolateUpdateTests(t)
	_, revision := updateMetadataFixture(t)
	clean := false
	updateReadInstalled = func() updateIdentity {
		return updateIdentity{Version: "v0.49.0", Revision: revision, Dirty: &clean, Path: filepath.Join(os.Getenv("HOME"), "installed")}
	}
	path, err := versionHoldPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	report, code := checkUpdate(t, "--check")
	if code != 2 || report.BlockedReason == "" || report.Status != "unknown" {
		t.Fatalf("unsafe hold bypassed: %+v %d", report, code)
	}
	if _, err := updateBinary("v0.49.0", false, true); err == nil {
		t.Fatal("unsafe hold bypassed during actual update")
	}
	if hold := gatherVersionReport(true).Hold; hold == nil || hold.Error == "" {
		t.Fatal("version card hid hold read error")
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(versionHoldEnv, "v0.48")
	if hold := resolveVersionHold(); hold.Error != "" || hold.Value != "v0.48" || hold.Source != versionHoldEnv {
		t.Fatalf("environment hold required a home path: %+v", hold)
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
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf called > '"+spy+"'\nexit 99\n"), 0o700); err != nil {
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
		return updateIdentity{Version: "v0.49.0", Revision: strings.Repeat("b", 40), Dirty: &clean, Path: filepath.Join(os.Getenv("HOME"), "installed")}
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
	if !called || result.Kind != "update" || result.Status != "updated" || result.After.Version != "" || result.ResolvedRelease != "v0.49.0" || result.After.Path != path || result.After.Revision != "" {
		t.Fatalf("false update receipt: %+v", result)
	}
	updateReadInstalled = func() updateIdentity {
		return updateIdentity{Version: "v0.49.0", Revision: revision, Dirty: &clean, Path: filepath.Join(os.Getenv("HOME"), "installed")}
	}
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
	for _, args := range [][]string{{"update"}, {"update", "--help"}, {"-o", "json", "update", "--help"}, {"--help", "update"}} {
		if err := runVersion(args); err == nil {
			t.Fatalf("retired version update was accepted: %v", args)
		}
	}
}

func TestUpdateMetadataSharesOneDeadlineAcrossLookups(t *testing.T) {
	isolateUpdateTests(t)
	clean := false
	updateReadInstalled = func() updateIdentity {
		return updateIdentity{Version: "v0.49.0", Revision: strings.Repeat("a", 40), Dirty: &clean, Path: filepath.Join(os.Getenv("HOME"), "installed")}
	}
	var deadline time.Time
	updateFetchLatest = func(ctx context.Context) (string, error) {
		var ok bool
		deadline, ok = ctx.Deadline()
		if !ok {
			t.Fatal("latest lookup has no deadline")
		}
		return "v0.49.0", nil
	}
	updateLookupRelease = func(ctx context.Context, _ string) error {
		if got, _ := ctx.Deadline(); got != deadline {
			t.Fatal("release lookup reset budget")
		}
		return nil
	}
	updateLookupRevision = func(ctx context.Context, _ string) (string, error) {
		if got, _ := ctx.Deadline(); got != deadline {
			t.Fatal("tag lookup reset budget")
		}
		return strings.Repeat("a", 40), nil
	}
	report := gatherUpdateCheck("cli", "", false, false)
	if report.Status != "current" {
		t.Fatalf("shared deadline check: %+v", report)
	}
}

func TestInstalledArtifactIdentityReadsMetadataWithoutExecuting(t *testing.T) {
	path, marker := buildUpdateArtifact(t, "v9.8.7")
	identity := installedArtifactIdentity(path)
	if identity.Version != "v9.8.7" || identity.Path != path {
		t.Fatalf("artifact metadata: %+v", identity)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("artifact was executed for metadata")
	}
}

func buildUpdateArtifact(t *testing.T, version string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	marker := filepath.Join(dir, "executed")
	source := "package main\nimport \"os\"\nvar Version string\nfunc main(){_ = os.WriteFile(" + fmt.Sprintf("%q", marker) + ",[]byte(Version),0600)}\n"
	main := filepath.Join(dir, "main.go")
	if err := os.WriteFile(main, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-ldflags", "-X main.Version="+version, "-o", path, main)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build identity fixture: %v %s", err, out)
	}
	return path, marker
}

func TestUnifiedUpdateUsesDestinationRatherThanInvokingProcess(t *testing.T) {
	path, marker := buildUpdateArtifact(t, "v9.8.7")
	isolateUpdateTests(t)
	updateDestination = func() (string, error) { return path, nil }
	updateReadInstalled = readInstalledUpdateIdentity
	before := Version
	Version = "v0.1.0"
	t.Cleanup(func() { Version = before })
	result, err := updateBinary("v9.7.0", false, false)
	if err == nil || !strings.Contains(err.Error(), "older than the installed v9.8.7") {
		t.Fatalf("downgrade guard used process identity: %+v %v", result, err)
	}
	if result.Before.Version != "v9.8.7" || result.Before.Path != path {
		t.Fatalf("receipt before was not disk identity: %+v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("destination binary was executed")
	}
}

func TestVersionCardComparesInvokingBuildWhenDiskDiffers(t *testing.T) {
	isolateUpdateTests(t)
	_, revision := updateMetadataFixture(t)
	clean := false
	before := Version
	Version = "v0.48.0"
	t.Cleanup(func() { Version = before })
	updateReadInvoking = func() updateIdentity { return updateIdentity{Version: "v0.48.0", Revision: revision, Dirty: &clean} }
	updateReadInstalled = func() updateIdentity { return updateIdentity{Version: "v0.49.0", Revision: revision, Dirty: &clean} }
	card := gatherVersionReport(false)
	check := gatherUpdateCheck("cli", "", false, false)
	if card.CLI.Installed != "v0.48.0" || card.CLIStatus != "update_available" || !card.Behind || check.Status != "current" {
		t.Fatalf("process and disk identities mixed: card=%+v check=%+v", card, check)
	}
}

func TestSDKCheckRefusesOversizedModuleWithoutWriting(t *testing.T) {
	isolateUpdateTests(t)
	dir := sdkUpdateFixture(t, "")
	file := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(file, []byte(strings.Repeat("x", maxMetadataBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	report, code := checkUpdate(t, "--sdk", "--check")
	if report.Status != "unknown" || code != 2 || report.Installed.Path != file || !strings.Contains(report.Reason, "1 MiB") {
		t.Fatalf("oversized module misreported: %+v %d", report, code)
	}
	if _, err := os.Stat(os.Getenv("SPARKWING_HOME")); !os.IsNotExist(err) {
		t.Fatal("oversized module check wrote state")
	}
}

func TestVersionCardUsesSharedComparisonWithoutRepeatingLookups(t *testing.T) {
	isolateUpdateTests(t)
	requests, revision := updateMetadataFixture(t)
	clean := false
	updateReadInstalled = func() updateIdentity {
		return updateIdentity{Version: "v0.49.0", Revision: strings.Repeat("b", 40), Dirty: &clean, Path: filepath.Join(os.Getenv("HOME"), "installed")}
	}
	lookups := 0
	updateFetchLatest = func(context.Context) (string, error) { lookups++; return "v0.49.0", nil }
	report := gatherVersionReport(false)
	if report.CLIStatus != "unknown" || report.CLIReason == "" || report.Behind {
		t.Fatalf("version comparison contradicted update check: %+v", report)
	}
	if lookups != 1 || requests.Load() != 2 {
		t.Fatalf("comparison repeated HTTP lookups: latest%d metadata%d", lookups, requests.Load())
	}
	text := captureStdout(t, func() { printVersionTable(report) })
	cli := strings.Split(text, "Project at ")[0]
	if strings.Contains(cli, "up to date") || !strings.Contains(cli, "comparison unknown") {
		t.Fatalf("card falsely claimed current: %s", cli)
	}
	updateReadInstalled = func() updateIdentity {
		return updateIdentity{Version: "v0.49.0", Revision: revision, Dirty: &clean, Path: filepath.Join(os.Getenv("HOME"), "installed")}
	}
	report = gatherVersionReport(false)
	if report.CLIStatus != "current" {
		t.Fatalf("verified release not current: %+v", report)
	}
	lookups = 0
	requests.Store(0)
	report = gatherVersionReport(true)
	if report.CLIStatus != "not_checked" || lookups != 0 || requests.Load() != 0 {
		t.Fatal("offline version performed a lookup")
	}
}

func TestUnifiedSDKUpdateUsesResolvedReleaseAndNativeGoSequence(t *testing.T) {
	isolateUpdateTests(t)
	updateMetadataFixture(t)
	dir := sdkUpdateFixture(t, "")
	bin := t.TempDir()
	log := filepath.Join(bin, "go-argv")
	script := "#!/bin/sh\nprintf '%s|%s|%s\\n' \"$PWD\" \"$GOTOOLCHAIN\" \"$*\" >> '" + log + "'\n" +
		"case \"$1\" in\nget) printf 'module fixture\\n\\ngo 1.26.0\\n\\nrequire " + sdkModulePath + " v0.49.0\\n' > go.mod;;\nmod) printf 'tidy diagnostic\\n'; printf 'fixture sum\\n' > go.sum;;\n*) exit 91;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GOTOOLCHAIN", "go1.26.6+auto")
	t.Setenv(versionHoldEnv, "v0.10")
	var commandError error
	out := captureStdout(t, func() { commandError = runUpdate([]string{"--sdk"}) })
	if commandError != nil {
		t.Fatal(commandError)
	}
	var receipt updateReceipt
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatalf("SDK progress polluted stdout: %v %q", err, out)
	}
	if receipt.Target != "sdk" || receipt.Before.Version != "v0.48.0" || receipt.After.Version != "v0.49.0" || receipt.After.Path != filepath.Join(dir, "go.mod") {
		t.Fatalf("SDK receipt does not reflect module: %+v", receipt)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := dir + "|go1.26.6+auto|get " + sdkModulePath + "@v0.49.0\n" + dir + "|go1.26.6+auto|mod tidy\n"
	if string(data) != want {
		t.Fatalf("Go resolver/toolchain semantics changed: %q", data)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), []byte("previous sum\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { commandError = runUpdate([]string{"--sdk"}) })
	if commandError != nil {
		t.Fatal(commandError)
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "updated" || receipt.Before.Version != receipt.After.Version {
		t.Fatalf("native Go work hidden behind current: %+v", receipt)
	}
	data, err = os.ReadFile(filepath.Join(dir, "go.sum"))
	if err != nil || string(data) != "fixture sum\n" {
		t.Fatal("same-pin fixture did not change go.sum")
	}
}

func TestUnifiedSDKPartialFailureHasNoSuccessReceipt(t *testing.T) {
	isolateUpdateTests(t)
	updateMetadataFixture(t)
	dir := sdkUpdateFixture(t, "")
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = get ]; then printf 'module fixture\\n\\ngo 1.26.0\\n\\nrequire " + sdkModulePath + " v0.49.0\\n' > go.mod; exit 0; fi\nexit 17\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var commandError error
	out := captureStdout(t, func() { commandError = runUpdate([]string{"--sdk"}) })
	if commandError == nil || out != "" || !strings.Contains(commandError.Error(), "SDK files may have changed") {
		t.Fatalf("partial SDK failure misreported: %v %q", commandError, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil || !strings.Contains(string(data), "v0.49.0") {
		t.Fatal("fixture did not exercise a partial module update")
	}
}

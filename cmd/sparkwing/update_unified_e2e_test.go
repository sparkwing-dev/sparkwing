//go:build e2e

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

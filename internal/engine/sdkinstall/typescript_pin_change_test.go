//go:build !windows

package sdkinstall

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

// npm ci --dry-run rewrites node_modules/.package-lock.json to the new lockfile without touching
// package folders, and npm install trusts that file, so a pin change over an existing tree must
// still install the new SDK. This is the tree `sparkwing pipeline upgrade` leaves behind.
func TestTypeScriptInstallFollowsAPinChangeOverAnExistingTree(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "pipeline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tarball := map[string]string{}
	for _, version := range []string{"0.68.0-qa.16", "0.68.0-qa.17"} {
		sdk := filepath.Join(root, "sdk-"+version)
		if err := os.Mkdir(sdk, 0o700); err != nil {
			t.Fatal(err)
		}
		writeInput(t, sdk, "package.json", []byte(`{"name":"@sparkwing/sdk","version":"`+version+`","type":"module","exports":"./index.js"}`))
		writeInput(t, sdk, "index.js", []byte(`export const version = "`+version+`";`))
		pack := exec.CommandContext(t.Context(), npm, "pack", "--pack-destination", dir)
		pack.Dir = sdk
		if output, err := pack.CombinedOutput(); err != nil {
			t.Fatalf("pack %s: %v: %s", version, err, output)
		}
		tarball[version] = "file:sparkwing-sdk-" + version + ".tgz"
	}
	writeInput(t, dir, "main.mjs", []byte(`import { version } from "@sparkwing/sdk"; console.log(version);`))
	pin := func(version string) []byte {
		t.Helper()
		writeInput(t, dir, "package.json", []byte(`{"main":"main.mjs","dependencies":{"@sparkwing/sdk":"`+tarball[version]+`"}}`))
		lock := exec.CommandContext(t.Context(), npm, "install", "--package-lock-only", "--offline", "--ignore-scripts", "--no-audit", "--no-fund")
		lock.Dir = dir
		if output, err := lock.CombinedOutput(); err != nil {
			t.Fatalf("lock %s: %v: %s", version, err, output)
		}
		body, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	// safety: upgrade resolves the new lockfile in a staged copy without node_modules, so the
	// repository tree sees only the manifest and lockfile change; lock B first, before any tree exists.
	lockB := pin("0.68.0-qa.17")
	pin("0.68.0-qa.16")
	request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engineid.TypeScriptSDK})
	if err != nil {
		t.Fatal(err)
	}
	install := func() {
		t.Helper()
		var output, diagnostics bytes.Buffer
		if err := Run(t.Context(), bytes.NewReader(request), &output, &diagnostics); err != nil {
			t.Fatalf("install: %v: %s", err, &diagnostics)
		}
	}
	installed := func() string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(dir, "node_modules", "@sparkwing", "sdk", "package.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct{ Version string }
		if err := json.Unmarshal(body, &manifest); err != nil {
			t.Fatal(err)
		}
		return manifest.Version
	}
	install()
	if got := installed(); got != "0.68.0-qa.16" {
		t.Fatalf("first install has SDK %s", got)
	}
	writeInput(t, dir, "package.json", []byte(`{"main":"main.mjs","dependencies":{"@sparkwing/sdk":"`+tarball["0.68.0-qa.17"]+`"}}`))
	writeInput(t, dir, "package-lock.json", lockB)
	install()
	if got := installed(); got != "0.68.0-qa.17" {
		t.Fatalf("after the pin moved to 0.68.0-qa.17 the tree still has SDK %s", got)
	}
}

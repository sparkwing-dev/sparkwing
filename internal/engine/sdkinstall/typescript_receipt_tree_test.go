//go:build !windows

package sdkinstall

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

// npm repairs a tree whose package folders are missing from its hidden lockfile's record, so a
// matching receipt must not hide a hand-deleted dependency from that repair.
func TestTypeScriptInstallRepairsAHandDeletedDependency(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sdk, dep, dir := filepath.Join(root, "sdk"), filepath.Join(root, "dep"), filepath.Join(root, "pipeline")
	for _, path := range []string{sdk, dep, dir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeInput(t, sdk, "package.json", []byte(`{"name":"@sparkwing/sdk","version":"0.68.0-qa.16","type":"module","exports":"./index.js"}`))
	writeInput(t, sdk, "index.js", []byte(`export const value = "ready";`))
	writeInput(t, dep, "package.json", []byte(`{"name":"dep","version":"1.0.0","main":"index.js"}`))
	writeInput(t, dep, "index.js", []byte(`module.exports = "dep";`))
	pack := exec.CommandContext(t.Context(), npm, "pack", "--pack-destination", dir)
	pack.Dir = dep
	if output, err := pack.CombinedOutput(); err != nil {
		t.Fatalf("pack: %v: %s", err, output)
	}
	writeInput(t, dir, "package.json", []byte(`{"main":"main.mjs","dependencies":{"@sparkwing/sdk":"file:../sdk","dep":"file:dep-1.0.0.tgz"}}`))
	writeInput(t, dir, "main.mjs", []byte(`import d from "dep"; console.log(d);`))
	lock := exec.CommandContext(t.Context(), npm, "install", "--package-lock-only", "--offline", "--ignore-scripts", "--no-audit", "--no-fund")
	lock.Dir = dir
	if output, err := lock.CombinedOutput(); err != nil {
		t.Fatalf("lock: %v: %s", err, output)
	}
	request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engineid.TypeScriptSDK})
	if err != nil {
		t.Fatal(err)
	}
	install := func() string {
		t.Helper()
		var output, diagnostics bytes.Buffer
		if err := Run(t.Context(), bytes.NewReader(request), &output, &diagnostics); err != nil {
			t.Fatalf("install: %v: %s", err, &diagnostics)
		}
		return diagnostics.String()
	}
	install()
	if diagnostics := install(); diagnostics != "" {
		t.Fatalf("unchanged verified tree ran npm again: %q", diagnostics)
	}
	dependency := filepath.Join(dir, "node_modules", "dep")
	if err := os.RemoveAll(dependency); err != nil {
		t.Fatal(err)
	}
	if diagnostics := install(); !strings.Contains(diagnostics, "added") {
		t.Fatalf("hand-deleted dependency reused the receipt: npm output %q", diagnostics)
	}
	if _, err := os.Stat(filepath.Join(dependency, "index.js")); err != nil {
		t.Fatalf("preparation left the dependency missing: %v", err)
	}
}

// An edit that lands while npm runs describes a tree npm never built, so it must not be recorded.
func TestTypeScriptInstallDoesNotRecordInputsEditedDuringNPM(t *testing.T) {
	f := newReceiptFixture(t)
	during := strings.Replace(receiptManifest, `{"main"`, `{"description":"edited during npm","main"`, 1)
	f.writeNPM(`if [ "$1" = install ] && [ -e arm ]; then rm arm; printf '%s' '` + during + `' > package.json; fi`)
	f.mustInstall()
	if f.mustInstall() {
		t.Fatal("unchanged verified tree ran npm again")
	}
	f.write(filepath.Join(f.dir, "package.json"), strings.Replace(receiptManifest, `{"main"`, `{"description":"before npm","main"`, 1))
	f.write(filepath.Join(f.dir, "arm"), "")
	if !f.mustInstall() {
		t.Fatal("changed manifest skipped npm")
	}
	if !f.mustInstall() {
		t.Fatal("a manifest edited while npm ran matched the receipt of the tree npm built")
	}
	if f.mustInstall() {
		t.Fatal("the reinstalled tree was not recorded for reuse")
	}
}

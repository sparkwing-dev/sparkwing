//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func TestSDKInstallTypeScriptReturnsNodeWithoutPreload(t *testing.T) {
	dir, bin := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"main":"pipeline.ts","dependencies":{"@sparkwing/sdk":"1.2.3"}}`)
	writeFile(t, filepath.Join(dir, "pipeline.ts"), "throw new Error('must not execute')")
	marker := filepath.Join(dir, "npm-invocation")
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$INSTALL_MARKER\"\nprintf 'installed\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(bin, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var diagnostics bytes.Buffer
	command, err := prepareInstalledSDK(t.Context(), dir, engine.TypeScriptSDK, []string{"PATH=" + bin, "INSTALL_MARKER=" + marker}, &diagnostics)
	if err != nil || command.Directory != dir || !reflect.DeepEqual(command.Command, []string{node, filepath.Join(dir, "pipeline.ts")}) {
		t.Fatalf("command: %+v %v", command, err)
	}
	called, err := os.ReadFile(marker)
	if err != nil || string(called) != "ci --no-audit --no-fund\n" || diagnostics.String() != "installed\n" {
		t.Fatalf("installer: %q %v %q", called, err, &diagnostics)
	}
}

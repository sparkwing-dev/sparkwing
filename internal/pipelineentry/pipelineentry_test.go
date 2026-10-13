package pipelineentry

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsTypeScriptNeedsTheSDKAsADependency(t *testing.T) {
	cases := map[string]struct {
		pkg  string
		want bool
	}{
		"no package.json":   {"", false},
		"tooling only":      {`{"devDependencies":{"prettier":"3"}}`, false},
		"sdk dependency":    {`{"dependencies":{"@sparkwing/sdk":"0.1.0"}}`, true},
		"sdk devDependency": {`{"devDependencies":{"@sparkwing/sdk":"0.1.0"}}`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.pkg != "" {
				writeFile(t, filepath.Join(dir, "package.json"), tc.pkg)
			}
			got, err := IsTypeScript(dir)
			if err != nil || got != tc.want {
				t.Fatalf("IsTypeScript = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), "{")
	if _, err := IsTypeScript(dir); err == nil {
		t.Fatal("a malformed package.json read as a Go pipeline")
	}
}

func TestTypeScriptPreloadsTheInstalledSDKBootstrap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the TypeScript SDK does not open Windows protocol handles yet")
	}
	node := filepath.Join(t.TempDir(), "node")
	writeFile(t, node, "#!/bin/sh\n")
	if err := os.Chmod(node, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(node))

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"main":"pipeline.ts","dependencies":{"@sparkwing/sdk":"file:../sdk"}}`)
	if _, err := TypeScript(dir); err == nil || !strings.Contains(err.Error(), "pipeline entry") {
		t.Fatalf("a missing entry file: %v", err)
	}
	writeFile(t, filepath.Join(dir, "pipeline.ts"), "")
	if _, err := TypeScript(dir); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("an uninstalled SDK: %v, want ErrNotInstalled", err)
	}

	// safety: a linked SDK must resolve to its real path, the name Node.js gives the module the SDK imports.
	sdk := t.TempDir()
	writeFile(t, filepath.Join(sdk, "package.json"), `{"exports":{".":"./src/index.ts"}}`)
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "@sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sdk, filepath.Join(dir, "node_modules", "@sparkwing", "sdk")); err != nil {
		t.Fatal(err)
	}
	if _, err := TypeScript(dir); err == nil || !strings.Contains(err.Error(), "./bootstrap") {
		t.Fatalf("an SDK without the bootstrap export: %v", err)
	}
	writeFile(t, filepath.Join(sdk, "package.json"), `{"exports":{".":"./src/index.ts","./bootstrap":"./src/bootstrap.ts"}}`)
	writeFile(t, filepath.Join(sdk, "src", "bootstrap.ts"), "")
	got, err := TypeScript(dir)
	if err != nil {
		t.Fatal(err)
	}
	realSDK, err := filepath.EvalSymlinks(sdk)
	if err != nil {
		t.Fatal(err)
	}
	want := Command{node, "--import", "file://" + filepath.ToSlash(filepath.Join(realSDK, "src", "bootstrap.ts")), filepath.Join(dir, "pipeline.ts")}
	if !slices.Equal(got, want) {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if args := got.Exec().Args; args[len(args)-1] != ProtocolArg || len(args) != len(want)+1 {
		t.Fatalf("exec args = %q", args)
	}

	writeFile(t, filepath.Join(dir, "package.json"), `{"dependencies":{"@sparkwing/sdk":"1"}}`)
	if _, err := TypeScript(dir); err == nil || !strings.Contains(err.Error(), `"main"`) {
		t.Fatalf("a package.json without main: %v", err)
	}
}

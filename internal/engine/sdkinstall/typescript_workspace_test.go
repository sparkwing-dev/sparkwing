package sdkinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestTypeScriptInstallWorkspaceConfiguration(t *testing.T) {
	for _, lockfile := range []string{"package-lock.json", "npm-shrinkwrap.json"} {
		for _, source := range []string{"project", "user", "global"} {
			t.Run(lockfile+"/"+source, func(t *testing.T) {
				dir := t.TempDir()
				selection, config := "workspaces=true", ".npmrc"
				if source != "project" {
					selection, config = "workspace[]=first", source+".npmrc"
					t.Setenv("npm_config_"+source+"config", filepath.Join(dir, config))
				}
				manifest := []byte(`{"private":true,"main":"main.mjs","workspaces":["packages/*"],"dependencies":{"@sparkwing/sdk":"file:./sdk"}}`)
				writeInput(t, dir, "package.json", manifest)
				writeInput(t, dir, "main.mjs", []byte(`import {value} from "@sparkwing/sdk"; console.log(value);`))
				for _, name := range []string{"sdk", "packages/first", "packages/second"} {
					if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				writeInput(t, dir, "sdk/package.json", []byte(`{"name":"@sparkwing/sdk","version":"0.68.0-qa.16","type":"module","exports":"./index.js"}`))
				writeInput(t, dir, "sdk/index.js", []byte(`export const value="ready";`))
				for _, name := range []string{"first", "second"} {
					writeInput(t, dir, "packages/"+name+"/package.json", fmt.Appendf(nil, `{"name":%q,"version":"1.0.0","scripts":{"postinstall":"node -e \"require('node:fs').writeFileSync('receipt','installed')\""}}`, name))
				}
				lock := exec.CommandContext(t.Context(), "npm", "install", "--package-lock-only", "--offline", "--ignore-scripts", "--no-audit", "--no-fund")
				lock.Dir = dir
				if output, err := lock.CombinedOutput(); err != nil {
					t.Fatalf("lock: %v: %s", err, output)
				}
				if lockfile != "package-lock.json" {
					if err := os.Rename(filepath.Join(dir, "package-lock.json"), filepath.Join(dir, lockfile)); err != nil {
						t.Fatal(err)
					}
				}
				originalLock, err := os.ReadFile(filepath.Join(dir, lockfile))
				if err != nil {
					t.Fatal(err)
				}
				request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engineid.TypeScriptSDK})
				if err != nil {
					t.Fatal(err)
				}
				install := func() {
					t.Helper()
					var output, diagnostics bytes.Buffer
					if err := Run(t.Context(), "v0.68.0-qa.16", bytes.NewReader(request), &output, &diagnostics); err != nil {
						t.Fatalf("workspace install: %v: %s", err, &diagnostics)
					}
					var command engine.SDKCommand
					if err := json.Unmarshal(output.Bytes(), &command); err != nil {
						t.Fatal(err)
					}
					result, err := exec.CommandContext(t.Context(), command.Command[0], command.Command[1:]...).CombinedOutput()
					if err != nil || string(result) != "ready\n" {
						t.Fatalf("workspace SDK import: %v: %s", err, result)
					}
				}
				writeInput(t, dir, config, []byte(selection+"\ninclude-workspace-root=true\nloglevel=verbose\n"))
				install()
				sdkPath := filepath.Join(dir, "node_modules", "@sparkwing", "sdk")
				installed, err := os.Lstat(sdkPath)
				if err != nil {
					t.Fatal(err)
				}
				install()
				reused, err := os.Lstat(sdkPath)
				if err != nil || !os.SameFile(installed, reused) {
					t.Fatalf("unchanged SDK installation was replaced: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "packages", "first", "receipt")); err != nil {
					t.Fatalf("first workspace did not install: %v", err)
				}
				second := filepath.Join(dir, "packages", "second", "receipt")
				if selection == "workspace[]=first" {
					if _, err := os.Stat(second); !os.IsNotExist(err) {
						t.Fatalf("unselected workspace ran: %v", err)
					}
					writeInput(t, dir, config, []byte("workspace[]=second\ninclude-workspace-root=true\n"))
					install()
				}
				if _, err := os.Stat(second); err != nil {
					t.Fatalf("second workspace did not install: %v", err)
				}
				if err := os.Remove(second); err != nil {
					t.Fatal(err)
				}
				writeInput(t, dir, config, []byte("workspaces=false\n"))
				install()
				if _, err := os.Stat(second); !os.IsNotExist(err) {
					t.Fatalf("disabled workspace ran: %v", err)
				}
				for path, original := range map[string][]byte{"package.json": manifest, lockfile: originalLock} {
					got, err := os.ReadFile(filepath.Join(dir, path))
					if err != nil || !bytes.Equal(got, original) {
						t.Fatalf("installer changed %s: %v", path, err)
					}
				}
				if err := os.Mkdir(filepath.Join(dir, "extra"), 0o700); err != nil {
					t.Fatal(err)
				}
				writeInput(t, dir, "extra/package.json", []byte(`{"name":"extra","version":"1.0.0"}`))
				writeInput(t, dir, "package.json", []byte(`{"main":"main.mjs","workspaces":["packages/*"],"dependencies":{"@sparkwing/sdk":"file:./sdk","extra":"file:./extra"}}`))
				var output, diagnostics bytes.Buffer
				if err := Run(t.Context(), "v0.68.0-qa.16", bytes.NewReader(request), &output, &diagnostics); err == nil {
					t.Fatal("SDK preparation accepted a dependency missing from the native lockfile")
				}
				preserved, err := os.Lstat(sdkPath)
				if err != nil || !os.SameFile(installed, preserved) {
					t.Fatalf("invalid lockfile disturbed the prepared SDK: %v", err)
				}
			})
		}
	}
}

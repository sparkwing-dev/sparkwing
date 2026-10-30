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

func TestRunTypeScriptLocalSDK(t *testing.T) {
	root := t.TempDir()
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Fatal(err)
	}
	sdk := filepath.Join(root, "sdk")
	if err := os.Mkdir(sdk, 0o700); err != nil {
		t.Fatal(err)
	}
	writeInput(t, sdk, "package.json", []byte(`{"name":"@sparkwing/sdk","version":"0.68.0-qa.16","type":"module","exports":"./index.js"}`))
	writeInput(t, sdk, "index.js", []byte(`export const value = "local SDK";`))
	command := exec.CommandContext(t.Context(), npm, "pack", "--ignore-scripts", "--offline", "--pack-destination", root)
	command.Dir = sdk
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("pack: %v: %s", err, output)
	}
	for i, source := range []string{"file:../sdk", "file:../sparkwing-sdk-0.68.0-qa.16.tgz", "file:" + filepath.ToSlash(sdk), "file:" + filepath.ToSlash(filepath.Join(root, "sparkwing-sdk-0.68.0-qa.16.tgz"))} {
		t.Run(source, func(t *testing.T) {
			dir := filepath.Join(root, fmt.Sprintf("pipeline-%d", i))
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			manifest, err := json.Marshal(map[string]any{"main": "pipeline.mjs", "dependencies": map[string]string{"@sparkwing/sdk": source}})
			if err != nil {
				t.Fatal(err)
			}
			writeInput(t, dir, "package.json", manifest)
			writeInput(t, dir, "pipeline.mjs", []byte(`import { value } from "@sparkwing/sdk"; console.log(value);`))
			command := exec.CommandContext(t.Context(), npm, "install", "--package-lock-only", "--offline", "--ignore-scripts", "--no-audit", "--no-fund")
			command.Dir = dir
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("lock: %v: %s", err, output)
			}
			request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engineid.TypeScriptSDK})
			if err != nil {
				t.Fatal(err)
			}
			var output, diagnostics bytes.Buffer
			if err := Run(t.Context(), "v0.68.0-qa.16", bytes.NewReader(request), &output, &diagnostics); err != nil {
				t.Fatalf("install: %v: %s", err, &diagnostics)
			}
			var installed engine.SDKCommand
			if err := json.Unmarshal(output.Bytes(), &installed); err != nil {
				t.Fatal(err)
			}
			result, err := exec.CommandContext(t.Context(), installed.Command[0], installed.Command[1:]...).CombinedOutput()
			if err != nil || string(result) != "local SDK\n" {
				t.Fatalf("SDK execution: %q: %v", result, err)
			}
		})
	}
}

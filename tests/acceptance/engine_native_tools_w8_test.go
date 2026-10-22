package acceptance_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engine/sdkinstall"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestEngineNativeNPMW8Components(t *testing.T) {
	if _, err := pipelineentry.NPMCommand(t.Context(), procenv.Env{}, runtime.GOOS, exec.LookPath, "--version"); err != nil {
		t.Skipf("native npm is not installed: %v", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("native Node.js is not installed: %v", err)
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("npm_config_cache", filepath.Join(root, "cache"))
	t.Setenv("npm_config_offline", "true")
	t.Setenv("npm_config_userconfig", filepath.Join(root, ".npmrc"))
	t.Setenv("npm_config_globalconfig", filepath.Join(root, "global-npmrc"))
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	w := tar.NewWriter(compressed)
	manifest := []byte(`{"name":"@sparkwing/sdk","version":"0.20.0"}`)
	if err := w.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o600, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"sdk.tgz":           archive.Bytes(),
		"package.json":      []byte(`{"name":"pipeline-fixture","version":"1.0.0","main":"main.js","dependencies":{"@sparkwing/sdk":"0.20.0"}}`),
		"package-lock.json": []byte(`{"name":"pipeline-fixture","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"pipeline-fixture","version":"1.0.0","dependencies":{"@sparkwing/sdk":"0.20.0"}},"node_modules/@sparkwing/sdk":{"version":"0.20.0","resolved":"file:sdk.tgz"}}}`),
		"main.js":           []byte("process.stdout.write('native SDK installation ready')"),
	} {
		if err := os.WriteFile(filepath.Join(root, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request, err := json.Marshal(engine.SDKInstallRequest{Directory: root, SDK: engine.TypeScriptSDK})
	if err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	if err := sdkinstall.Run(t.Context(), "v0.20.0", bytes.NewReader(request), &output, &diagnostics); err != nil {
		t.Fatalf("native npm install: %v: %s", err, &diagnostics)
	}
	var result engine.SDKCommand
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Directory != root || len(result.Command) != 2 || result.Command[1] != filepath.Join(root, "main.js") {
		t.Fatalf("native SDK command: %+v", result)
	}
	command := exec.CommandContext(t.Context(), result.Command[0], result.Command[1:]...)
	command.Dir = result.Directory
	if output, err := command.CombinedOutput(); err != nil || string(output) != "native SDK installation ready" {
		t.Fatalf("native Node.js entry: %s: %v", output, err)
	}
}

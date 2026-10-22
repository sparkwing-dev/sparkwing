package sdkinstall

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func TestRunGoSDKNeedsOnlyThePipelineDependencyArchives(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SPARKWING_HOME", filepath.Join(root, "home"))
	t.Setenv("GOMODCACHE", filepath.Join(root, "modules"))
	t.Setenv("GOWORK", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOPROXY", "off")
	dir := filepath.Join(root, "pipeline")
	sdk := filepath.Join(root, "sdk")
	for _, path := range []string{dir, sdk} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeInput(t, sdk, "go.mod", []byte("module "+engine.GoModule+"\ngo 1.26\n"))
	writeInput(t, dir, "go.mod", []byte("module fixture\ngo 1.26\nrequire (\n"+engine.GoModule+" v0.20.0\nexample.test/unused v1.0.0\n)\nreplace "+engine.GoModule+" => "+filepath.ToSlash(sdk)+"\n"))
	writeInput(t, dir, "main.go", []byte("package main\nfunc main(){print(\"built offline\")}\n"))
	request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engine.GoSDK, ExecutableDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	if err := Run(t.Context(), "development", bytes.NewReader(request), &output, &diagnostics); err != nil {
		t.Fatalf("unneeded module blocked pipeline installation: %v: %s", err, &diagnostics)
	}
	var installed engine.SDKCommand
	if err := json.Unmarshal(output.Bytes(), &installed); err != nil {
		t.Fatal(err)
	}
	result, err := exec.CommandContext(t.Context(), installed.Command[0]).CombinedOutput()
	if err != nil || string(result) != "built offline" {
		t.Fatalf("installed pipeline: %q %v", result, err)
	}
	writeInput(t, dir, "main.go", []byte("package main\nimport _ \"example.test/unused\"\nfunc main(){}\n"))
	output.Reset()
	diagnostics.Reset()
	if err := Run(t.Context(), "development", bytes.NewReader(request), &output, &diagnostics); err == nil || output.Len() != 0 || !strings.Contains(diagnostics.String(), "example.test/unused") {
		t.Fatalf("missing imported module did not refuse installation: %s %s %v", &output, &diagnostics, err)
	}
}

func TestRunGoSDKPreparesEffectiveInputsAndOwnsExecutable(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	t.Setenv("HOME", root)
	t.Setenv("SPARKWING_HOME", filepath.Join(root, "home"))
	t.Setenv("GOPATH", filepath.Join(root, "gopath"))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOWORK", "off")
	proxy := filepath.Join(root, "proxy")
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	writeProxyModule(t, proxy, engine.GoModule, "v0.20.0", "package sdk\nconst Value=\"sdk-\"\n")
	writeProxyModule(t, proxy, "example.com/spark", "v1.0.0", "package spark\nconst Value=\"base\"\n")
	writeProxyModule(t, proxy, "example.com/spark", "v1.1.0", "package spark\nconst Value=\"overlay\"\n")
	dir := filepath.Join(root, "pipeline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	module := "module fixture\ngo 1.26\nrequire (\n" + engine.GoModule + " v0.20.0\nexample.com/spark v1.0.0\n)\n"
	writeInput(t, dir, "go.mod", []byte(module))
	writeInput(t, dir, ".resolved.mod", []byte(strings.ReplaceAll(module, "v1.0.0", "v1.1.0")))
	writeInput(t, dir, "main.go", []byte("package main\nimport (\"fmt\"; sdk \""+engine.GoModule+"\"; \"example.com/spark\")\nfunc main(){fmt.Print(sdk.Value+spark.Value)}\n"))
	install := func() engine.SDKCommand {
		t.Helper()
		request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engine.GoSDK, ExecutableDirectory: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		var output, diagnostics bytes.Buffer
		if err := Run(t.Context(), "v0.20.0", bytes.NewReader(request), &output, &diagnostics); err != nil {
			t.Fatalf("real Go installation: %v: %s", err, &diagnostics)
		}
		var command engine.SDKCommand
		if err := json.Unmarshal(output.Bytes(), &command); err != nil {
			t.Fatal(err)
		}
		return command
	}
	first := install()
	sum, err := os.ReadFile(filepath.Join(dir, ".resolved.sum"))
	if err != nil || len(sum) == 0 {
		t.Fatalf("effective checksums were not prepared: %s %v", sum, err)
	}
	second := install()
	if first.Command[0] == second.Command[0] {
		t.Fatal("installations share a pruneable executable instead of owning execution bytes")
	}
	entries, err := bincache.ScanCache()
	if err != nil || len(entries) != 1 {
		t.Fatalf("prepared inputs did not stabilize the cache key: %+v %v", entries, err)
	}
	options := bincache.PruneOptions{ReclaimEntries: 100, MaxEntries: 100}
	pruned, err := bincache.Prune(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if pruned.ReclaimedEntries == 0 {
		t.Fatalf("cache prune did not exercise materialized entry: %+v", pruned)
	}
	for _, command := range []engine.SDKCommand{first, second} {
		output, err := exec.Command(command.Command[0]).CombinedOutput()
		if err != nil || string(output) != "sdk-overlay" {
			t.Fatalf("effective SDK execution after prune: %q %v", output, err)
		}
	}
	replacement := filepath.Join(root, "replacement")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	writeInput(t, replacement, "go.mod", []byte("module "+engine.GoModule+"\ngo 1.26\n"))
	overlay := strings.ReplaceAll(module, "v1.0.0", "v1.1.0") + "replace " + engine.GoModule + " => " + filepath.ToSlash(replacement) + "\n"
	writeInput(t, dir, ".resolved.mod", []byte(overlay))
	property := func(value string) bool {
		writeInput(t, replacement, "entry.go", []byte(fmt.Sprintf("package sdk\nconst Value=%q\n", value)))
		request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engine.GoSDK, ExecutableDirectory: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		var output, diagnostics bytes.Buffer
		err = Run(t.Context(), "v0.20.0", bytes.NewReader(request), &output, &diagnostics)
		if err == nil || output.Len() != 0 {
			t.Logf("replacement produced executable: %s %v", &output, err)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 8}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, ".resolved.mod")); err != nil {
		t.Fatal(err)
	}
	writeInput(t, replacement, "entry.go", []byte("package sdk\nconst Value=\"poison-\"\n"))
	writeInput(t, dir, "poison.mod", []byte(module+"replace "+engine.GoModule+" => "+filepath.ToSlash(replacement)+"\n"))
	t.Setenv("GOFLAGS", os.Getenv("GOFLAGS")+" -modfile="+filepath.Join(dir, "poison.mod"))
	protected := install()
	output, err := exec.Command(protected.Command[0]).CombinedOutput()
	if err != nil || string(output) != "sdk-base" {
		t.Fatalf("ambient modfile changed effective inputs: %q %v", output, err)
	}
}

func writeProxyModule(t *testing.T, proxy, module, version, source string) {
	t.Helper()
	dir := filepath.Join(proxy, filepath.FromSlash(module), "@v")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mod := []byte("module " + module + "\ngo 1.26\n")
	writeInput(t, dir, version+".mod", mod)
	writeInput(t, dir, version+".info", []byte(fmt.Sprintf(`{"Version":%q,"Time":"2026-01-01T00:00:00Z"}`, version)))
	var body bytes.Buffer
	archive := zip.NewWriter(&body)
	for path, content := range map[string][]byte{"go.mod": mod, "entry.go": []byte(source)} {
		writer, err := archive.Create(module + "@" + version + "/" + path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	writeInput(t, dir, version+".zip", body.Bytes())
}

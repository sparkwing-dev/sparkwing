package sdkinstall

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func TestSDKInstallUncachedUsesRealCompilerWithoutPublishingCacheEntry(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: compiles a real Go executable")
	}
	dir := realBuildPolicyFixture(t)
	command := runBuildPolicyInstall(t, dir, `"no_bincache":true`)
	if command.CachedExecutable != "" {
		t.Fatalf("uncached build published %s", command.CachedExecutable)
	}
	info, err := buildinfo.ReadFile(command.Command[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range info.Settings {
		if setting.Key == "-trimpath" && setting.Value == "true" || setting.Key == "-ldflags" && strings.Contains(setting.Value, "-s -w") {
			t.Fatalf("uncached build lost debug information: %+v", setting)
		}
	}
	assertBuildPolicyOutput(t, command, "first")
}

func TestSDKInstallHashAllFilesIncludesIgnoredEmbeddedInputs(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: compiles real Go executables")
	}
	dir := realBuildPolicyFixture(t)
	first := runBuildPolicyInstall(t, dir, "")
	assertBuildPolicyOutput(t, first, "first")
	writeInput(t, dir, "payload", []byte("second"))
	cached := runBuildPolicyInstall(t, dir, "")
	if cached.CachedExecutable != first.CachedExecutable {
		t.Fatal("fixture did not reuse the ignored-input cache entry")
	}
	assertBuildPolicyOutput(t, cached, "first")
	all := runBuildPolicyInstall(t, dir, `"hash_all_files":true`)
	assertBuildPolicyOutput(t, all, "second")
	if all.CachedExecutable == first.CachedExecutable {
		t.Fatal("hash-all-files reused a stale executable")
	}
	ordinary := runBuildPolicyInstall(t, dir, "")
	if ordinary.CachedExecutable != first.CachedExecutable {
		t.Fatal("hash-all-files changed another installation's policy")
	}
	assertBuildPolicyOutput(t, ordinary, "first")
}

func realBuildPolicyFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	dir := t.TempDir()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	writeInput(t, dir, "go.mod", []byte(fmt.Sprintf("module fixture\ngo 1.26\nrequire %s v0.20.0\nreplace %s => %q\n", engine.GoModule, engine.GoModule, root)))
	writeInput(t, dir, "main.go", []byte("package main\nimport (_ \"embed\";\"fmt\")\n//go:embed payload\nvar payload string\nfunc main(){fmt.Print(payload)}\n"))
	writeInput(t, dir, "payload", []byte("first"))
	writeInput(t, dir, ".gitignore", []byte("payload\n"))
	command := exec.CommandContext(t.Context(), "git", "-C", dir, "init", "--quiet")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return dir
}

func runBuildPolicyInstall(t *testing.T, dir, policy string) engine.SDKCommand {
	t.Helper()
	body, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engine.GoSDK, ExecutableDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if policy != "" {
		body = append(body[:len(body)-1], []byte(","+policy+"}")...)
	}
	var stdout, stderr bytes.Buffer
	if err := Run(t.Context(), "v0.20.0", bytes.NewReader(body), &stdout, &stderr); err != nil {
		t.Fatalf("install: %v: %s", err, &stderr)
	}
	var command engine.SDKCommand
	if err := json.Unmarshal(stdout.Bytes(), &command); err != nil {
		t.Fatal(err)
	}
	return command
}

func assertBuildPolicyOutput(t *testing.T, installed engine.SDKCommand, want string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), installed.Command[0], installed.Command[1:]...)
	command.Env = []string{}
	body, err := command.CombinedOutput()
	if err != nil || string(body) != want {
		t.Fatalf("compiled output: %q, want %q: %v", body, want, err)
	}
	if _, err := os.Stat(installed.Command[0]); err != nil {
		t.Fatal(err)
	}
}

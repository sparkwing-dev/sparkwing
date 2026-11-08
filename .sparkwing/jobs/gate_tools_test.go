package jobs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCloudGateToolInstallVerifiesDownloadsAndReusesInstalledTools(t *testing.T) {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	const body = "#!/bin/sh\nexit 0\n"
	if err := tw.WriteHeader(&tar.Header{Name: "release/tool", Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(fixture, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	curl := filepath.Join(bin, "curl")
	if err := os.WriteFile(curl, []byte("#!/bin/sh\ncp "+shellQuote(fixture)+" \"$4\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	tool := gateToolArchive{"fixture", "https://example.invalid/tool", fmt.Sprintf("%x", sha256.Sum256(archive.Bytes())), "release/tool", "tool"}
	bad := tool
	bad.checksum = strings.Repeat("0", 64)
	if err := installGateTool(context.Background(), root, bad); err == nil {
		t.Fatal("a download with the wrong checksum was installed")
	}
	if err := installGateTool(context.Background(), root, tool); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(curl, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installGateTool(context.Background(), root, tool); err != nil {
		t.Fatalf("an installed cached tool attempted another download: %v", err)
	}
}

func TestCloudGateToolsCoverBothRunnerArchitectures(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		tools, err := cloudGateTools(arch)
		if err != nil || len(tools) != 4 {
			t.Fatalf("%s tools=%+v error=%v", arch, tools, err)
		}
		for _, tool := range tools {
			if len(tool.checksum) != 64 || tool.executable == "" {
				t.Fatalf("%s has an unpinned or unusable tool: %+v", arch, tool)
			}
		}
	}
	if _, err := cloudGateTools("unsupported"); err == nil {
		t.Fatal("unsupported architecture accepted")
	}
}

func TestCloudPnpmEntrypointMatchesItsPinnedPackage(t *testing.T) {
	data, err := os.ReadFile("testdata/pnpm-12.6.0-package.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Bin map[string]string `json:"bin"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	tools, err := cloudGateTools("amd64")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.name == "pnpm" && tool.executable != manifest.Bin["pnpm"] {
			t.Fatalf("pnpm executable %q differs from its published package entrypoint %q", tool.executable, manifest.Bin["pnpm"])
		}
	}
}

func TestCloudGoBudgetUsesTheDeclaredCoresBeforeChecks(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	t.Setenv("CI", "")
	t.Setenv("GOMAXPROCS", "1")
	if err := configureCloudGoBudget(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOMAXPROCS(0) != overlappedGoSuiteCPUs || !currentHost().singleTenant {
		t.Fatalf("runtime budget=%d host=%+v", runtime.GOMAXPROCS(0), currentHost())
	}
	if got := goStepParallelism(currentHost()); got != goStepParallelism(hostShape{cpus: min(runtime.NumCPU(), overlappedGoSuiteCPUs), singleTenant: true}) {
		t.Fatalf("Go workers=%d", got)
	}
}

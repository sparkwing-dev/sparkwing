package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	gateToolsPath   = ".sparkwing/cache/gate-tools"
	gateGoCachePath = ".sparkwing/cache/.go/build"
)

type gateToolArchive struct {
	name, url, checksum, entry, executable string
}

func cloudGateTools(arch string) ([]gateToolArchive, error) {
	checksum := map[string]string{
		"amd64": "8df580d2670fed8fa984aac0507099af8df275e665215f5c7a2ae3943893a553",
		"arm64": "44cd40a8c76c86755375adfeea52cfd3533cb43d7bd647771e0ae065e166df3a",
	}[arch]
	if checksum == "" {
		return nil, fmt.Errorf("cloud gate tools: unsupported Linux architecture %s", arch)
	}
	nativeArch := "x64"
	nativeChecksum := "2b5f62986b14f2891fb78e5b554e60e38f25987217fed578f0303bbfda5e124a"
	if arch == "arm64" {
		nativeArch = "arm64"
		nativeChecksum = "22b886e280579171aa77e2df699792932ccf66b1853aa73cf1cd851c959465ac"
	}
	zigArch := "x86_64"
	zigChecksum := "02aa270f183da276e5b5920b1dac44a63f1a49e55050ebde3aecc9eb82f93239"
	if arch == "arm64" {
		zigArch = "aarch64"
		zigChecksum = "958ed7d1e00d0ea76590d27666efbf7a932281b3d7ba0c6b01b0ff26498f667f"
	}
	lint := "golangci-lint-2.12.2-linux-" + arch
	return []gateToolArchive{
		{"golangci-lint", "https://github.com/golangci/golangci-lint/releases/download/v2.12.2/" + lint + ".tar.gz", checksum, lint + "/golangci-lint", "golangci-lint"},
		{"pnpm-data", "https://registry.npmjs.org/pnpm/-/pnpm-12.6.0.tgz", "05b7b921fbb31564505c967eabf825895a1cc18f50935c00be98815272cc9d56", "", "package.json"},
		{"pnpm", "https://registry.npmjs.org/@pnpm/exe.linux-" + nativeArch + "/-/exe.linux-" + nativeArch + "-12.6.0.tgz", nativeChecksum, "package/pnpm", "pnpm"},
		{"zig", "https://ziglang.org/download/0.15.2/zig-" + zigArch + "-linux-0.15.2.tar.xz", zigChecksum, "", "zig"},
	}, nil
}

func installGateTool(ctx context.Context, root string, tool gateToolArchive) error {
	dir := filepath.Join(root, tool.name)
	if _, err := os.Stat(filepath.Join(dir, tool.executable)); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entry := ""
	if tool.entry != "" {
		entry = shellQuote(tool.entry)
	}
	script := fmt.Sprintf(`set -eu
archive=$(mktemp)
trap 'rm -f "$archive"' EXIT
curl -fsSL %s -o "$archive"
printf '%%s  %%s\n' %s "$archive" | sha256sum -c -
tar -xf "$archive" --no-same-owner --strip-components=1 -C %s %s
`, shellQuote(tool.url), shellQuote(tool.checksum), shellQuote(dir), entry)
	if _, err := sparkwing.Bash(ctx, script).Run(); err != nil {
		return fmt.Errorf("install Cloud gate tool %s: %w", tool.name, err)
	}
	return nil
}

func prepareCloudGateTools(ctx context.Context, goCache string) error {
	for _, name := range []string{"bash", "git", "go", "curl", "tar", "sha256sum", "xz", "node", "npm", "diff"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("cloud gate requires runner image tool %s: %w", name, err)
		}
	}
	tools, err := cloudGateTools(runtime.GOARCH)
	if err != nil {
		return err
	}
	root := sparkwing.Path(gateToolsPath)
	for _, tool := range tools {
		if err := installGateTool(ctx, root, tool); err != nil {
			return err
		}
	}
	pnpm := filepath.Join(root, "pnpm")
	if _, err := sparkwing.Exec(ctx, "ln", "-sfn", "../pnpm-data/dist", filepath.Join(pnpm, "dist")).Run(); err != nil {
		return err
	}
	if err := os.Setenv("PATH", strings.Join([]string{filepath.Join(root, "golangci-lint"), pnpm, filepath.Join(root, "zig"), os.Getenv("PATH")}, string(os.PathListSeparator))); err != nil {
		return err
	}
	if err := os.Setenv("GOCACHE", sparkwing.Path(goCache)); err != nil {
		return err
	}
	if err := os.Setenv("CGO_ENABLED", "1"); err != nil {
		return err
	}
	if err := os.Setenv("CC", "zig cc"); err != nil {
		return err
	}
	for _, command := range [][]string{{"golangci-lint", "version"}, {"pnpm", "--version"}, {"go", "version"}, {"node", "--version"}, {"npm", "--version"}, {"zig", "version"}, {"go", "env", "CGO_ENABLED", "CC"}} {
		if _, err := sparkwing.Exec(ctx, command[0], command[1:]...).Run(); err != nil {
			return fmt.Errorf("cloud gate tool preflight: %w", err)
		}
	}
	return nil
}

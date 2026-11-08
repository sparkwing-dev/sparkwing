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
	lint := "golangci-lint-2.12.2-linux-" + arch
	return []gateToolArchive{
		{"golangci-lint", "https://github.com/golangci/golangci-lint/releases/download/v2.12.2/" + lint + ".tar.gz", checksum, lint + "/golangci-lint", "golangci-lint"},
		{"pnpm", "https://registry.npmjs.org/pnpm/-/pnpm-12.6.0.tgz", "05b7b921fbb31564505c967eabf825895a1cc18f50935c00be98815272cc9d56", "", "bin/pnpm.cjs"},
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
tar -xzf "$archive" --no-same-owner --strip-components=1 -C %s %s
`, shellQuote(tool.url), shellQuote(tool.checksum), shellQuote(dir), entry)
	if _, err := sparkwing.Bash(ctx, script).Run(); err != nil {
		return fmt.Errorf("install Cloud gate tool %s: %w", tool.name, err)
	}
	return nil
}

func prepareCloudGateTools(ctx context.Context) error {
	for _, name := range []string{"bash", "git", "go", "curl", "tar", "sha256sum", "node", "npm", "diff"} {
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
	pnpm := filepath.Join(root, "pnpm", "bin")
	if _, err := sparkwing.Exec(ctx, "ln", "-sf", "pnpm.cjs", filepath.Join(pnpm, "pnpm")).Run(); err != nil {
		return err
	}
	if err := os.Setenv("PATH", strings.Join([]string{filepath.Join(root, "golangci-lint"), pnpm, os.Getenv("PATH")}, string(os.PathListSeparator))); err != nil {
		return err
	}
	if err := os.Setenv("GOCACHE", sparkwing.Path(gateGoCachePath)); err != nil {
		return err
	}
	for _, command := range [][]string{{"golangci-lint", "version"}, {"pnpm", "--version"}, {"go", "version"}, {"node", "--version"}, {"npm", "--version"}} {
		if _, err := sparkwing.Exec(ctx, command[0], command[1:]...).Run(); err != nil {
			return fmt.Errorf("cloud gate tool preflight: %w", err)
		}
	}
	return nil
}

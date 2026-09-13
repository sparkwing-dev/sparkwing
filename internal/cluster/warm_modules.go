package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const warmSDKModule = "github.com/sparkwing-dev/sparkwing"

const warmModulesOff = "off"

func parseWarmModules(spec, sdkVersion string) ([]string, error) {
	spec = strings.TrimSpace(spec)
	if strings.EqualFold(spec, warmModulesOff) {
		return nil, nil
	}
	if spec == "" {
		if !semver.IsValid(sdkVersion) {
			return nil, nil
		}
		return []string{warmSDKModule + "@" + sdkVersion}, nil
	}

	entries := splitCSV(spec)
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.ContainsAny(entry, " \t") {
			return nil, fmt.Errorf("module %q contains whitespace", entry)
		}
		path, version, hasVersion := strings.Cut(entry, "@")
		if path == "" {
			return nil, fmt.Errorf("module %q has no module path", entry)
		}
		if !hasVersion {
			version = warmDefaultVersion(path, sdkVersion)
		}
		if version == "" {
			return nil, fmt.Errorf("module %q has an empty version", entry)
		}
		if version != "latest" && !semver.IsValid(version) {
			return nil, fmt.Errorf("module %q: %q is neither a semantic version nor \"latest\"", entry, version)
		}
		out = append(out, path+"@"+version)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func warmDefaultVersion(path, sdkVersion string) string {
	if path == warmSDKModule && semver.IsValid(sdkVersion) {
		return sdkVersion
	}
	return "latest"
}

func warmModuleCache(ctx context.Context, modules []string, logger *slog.Logger) {
	if len(modules) == 0 {
		return
	}
	if _, err := exec.LookPath("go"); err != nil {
		logger.Warn("module warm skipped; no go toolchain on PATH", "err", err)
		return
	}
	// safety: `go mod download path@version` resolves only outside a module, and
	// the runner's working directory is a repository checkout often enough.
	dir, err := os.MkdirTemp("", "sparkwing-warm-")
	if err != nil {
		logger.Warn("module warm skipped; no writable temporary directory", "err", err)
		return
	}
	defer func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			logger.Warn("module warm: scratch directory left behind", "dir", dir, "err", rmErr)
		}
	}()

	start := time.Now()
	cmd := exec.CommandContext(ctx, "go", append([]string{"mod", "download"}, modules...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if err != nil {
		logger.Warn("module warm failed; the next compile pays the full download",
			"modules", modules, "warm_ms", elapsed.Milliseconds(), "err", err, "output", strings.TrimSpace(string(out)))
		return
	}
	logger.Info("module cache warmed", "modules", modules, "warm_ms", elapsed.Milliseconds())
}

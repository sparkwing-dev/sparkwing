package nodehost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/engine/npmcmd"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// CacheDirectory resolves package-manager caches using the persisted job environment.
func (e SDKEnvironment) CacheDirectory(ctx context.Context, input NodeInput, cache nodeproto.CacheSpec) (string, error) {
	if cache.Resolver != depcache.ResolverGoModules && cache.Resolver != depcache.ResolverNpm || input.Entry == nil || input.Entry.Directory == "" {
		return "", errors.New("cache resolution requires a known resolver and workspace")
	}
	env, err := e.job(ctx, procenv.StepTool, input.Accepted, input.NodeID, input.Generation, input.SpecHash, input.GrantedRunner)
	if err != nil {
		return "", err
	}
	lookup := func(name string) string {
		for _, assignment := range env {
			key, value, _ := strings.Cut(assignment, "=")
			if key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name) {
				return value
			}
		}
		return ""
	}
	variable := "GOMODCACHE"
	if cache.Resolver == depcache.ResolverNpm {
		variable = "npm_config_cache"
	}
	if dir := lookup(variable); dir != "" {
		return filepath.Clean(dir), nil
	}
	lookPath := func(name string) (string, error) {
		return cacheToolPath(name, lookup("PATH"), lookup("PATHEXT"), input.Entry.Directory)
	}
	var command *exec.Cmd
	if cache.Resolver == depcache.ResolverNpm {
		command, err = npmcmd.ForPlatform(runtime.GOOS, lookPath, "config", "get", "cache")
	} else {
		var executable string
		executable, err = lookPath("go")
		if err == nil {
			command = exec.CommandContext(ctx, executable, "env", "GOMODCACHE")
		}
	}
	if err == nil {
		command.Env, command.Dir = env, input.Entry.Directory
		var output bytes.Buffer
		command.Stdout = &output
		if err := gotoolchain.Run(ctx, command); err == nil {
			if dir := strings.TrimSpace(output.String()); dir != "" && dir != "undefined" {
				return filepath.Clean(dir), nil
			}
		}
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	return defaultCacheDirectory(cache.Resolver, lookup)
}

func cacheToolPath(name, path, extensions, workdir string) (string, error) {
	suffixes := []string{""}
	if runtime.GOOS == "windows" {
		if extensions == "" {
			extensions = ".COM;.EXE;.BAT;.CMD"
		}
		suffixes = strings.Split(extensions, ";")
	}
	for _, directory := range filepath.SplitList(path) {
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(workdir, directory)
		}
		for _, suffix := range suffixes {
			candidate := filepath.Join(directory, name+suffix)
			info, err := os.Stat(candidate)
			if err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0) {
				return candidate, nil
			}
		}
	}
	return "", errors.New("cache resolver tool is unavailable")
}

func defaultCacheDirectory(resolver string, lookup func(string) string) (string, error) {
	home := lookup("HOME")
	if runtime.GOOS == "windows" {
		if resolver == depcache.ResolverNpm && lookup("LOCALAPPDATA") != "" {
			return filepath.Join(lookup("LOCALAPPDATA"), "npm-cache"), nil
		}
		if profile := lookup("USERPROFILE"); profile != "" {
			home = profile
		}
	}
	if home == "" {
		return "", errors.New("cache resolver environment has no home directory")
	}
	if resolver == depcache.ResolverGoModules {
		return filepath.Join(home, "go", "pkg", "mod"), nil
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "AppData", "Local", "npm-cache"), nil
	}
	return filepath.Join(home, ".npm"), nil
}

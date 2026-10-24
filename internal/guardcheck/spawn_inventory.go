package main

import (
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

func loadSpawnPackages(repo string, visit func(*packages.Package)) ([]string, error) {
	repo, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	files, err := spawnSourceInventory(repo)
	if err != nil {
		return nil, err
	}
	covered := make(map[string]bool)
	load := func(platform, arch, cgo, tags string, patterns []string) error {
		cfg := &packages.Config{
			Dir:        repo,
			Env:        append(os.Environ(), "GOWORK=off", "GOOS="+platform, "GOARCH="+arch, "CGO_ENABLED="+cgo),
			BuildFlags: []string{"-tags=" + tags},
			Mode:       packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		}
		pkgs, err := packages.Load(cfg, patterns...)
		if err != nil {
			return err
		}
		for _, pkg := range pkgs {
			if len(pkg.Errors) > 0 || pkg.IllTyped || len(pkg.GoFiles) > 0 && len(pkg.Syntax) == 0 {
				return fmt.Errorf("spawn guard (%s/%s, cgo=%s, tags=%s): loading %s: %v (ill-typed=%t)", platform, arch, cgo, tags, pkg.PkgPath, pkg.Errors, pkg.IllTyped)
			}
			visit(pkg)
			// Cgo syntax is rewritten into generated compiled files; GoFiles
			// retains the original sources represented by those typed trees.
			for _, name := range pkg.GoFiles {
				covered[filepath.Clean(name)] = true
			}
		}
		return nil
	}
	for _, target := range supportedPlatforms {
		platform, arch, _ := strings.Cut(target, "/")
		if err := load(platform, arch, "0", "", []string{"./..."}); err != nil {
			return nil, err
		}
	}
	if err := load(runtime.GOOS, runtime.GOARCH, "0", "qatrust", []string{"./..."}); err != nil {
		return nil, err
	}
	if err := load(runtime.GOOS, runtime.GOARCH, "1", "", []string{"./..."}); err != nil {
		return nil, err
	}
	// BSD exercises the repository's fallback Unix implementations without
	// requiring its Linux-only commands to support an additional platform.
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled, ctx.BuildTags = "freebsd", "amd64", false, nil
	dirs := make(map[string]bool)
	for _, name := range files {
		if covered[name] {
			continue
		}
		matches, err := ctx.MatchFile(filepath.Dir(name), filepath.Base(name))
		if err != nil {
			return nil, err
		}
		if matches {
			rel, err := filepath.Rel(repo, filepath.Dir(name))
			if err != nil {
				return nil, err
			}
			dirs["./"+filepath.ToSlash(rel)] = true
		}
	}
	var patterns []string
	for dir := range dirs {
		patterns = append(patterns, dir)
	}
	sort.Strings(patterns)
	if len(patterns) > 0 {
		if err := load("freebsd", "amd64", "0", "", patterns); err != nil {
			return nil, err
		}
	}
	var findings []string
	for _, name := range files {
		if !covered[name] {
			findings = append(findings, name+": spawn boundary: production Go source was not type-analyzed; add its build configuration to the guard or remove unreachable source")
		}
	}
	return findings, nil
}

func spawnSourceInventory(repo string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != repo {
				if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
					return filepath.SkipDir
				}
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				} else if !os.IsNotExist(err) {
					return err
				}
			}
		} else if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") && !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

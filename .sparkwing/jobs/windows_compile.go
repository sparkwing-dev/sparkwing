package jobs

import (
	"context"
	"fmt"
	"go/build"
	"go/build/constraint"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func runWindowsCompile(ctx context.Context) error {
	files, _, err := affectedChangeScope(ctx)
	if err != nil {
		return err
	}
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		return err
	}
	var goFiles []string
	for _, file := range files {
		if strings.HasSuffix(file, ".go") && !strings.Contains(file, "node_modules/") {
			goFiles = append(goFiles, file)
		}
	}
	return runWindowsCompileTargets(ctx, touchedPackageTargets(goFiles, modules))
}

func windowsCompileArchitectures(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	windows, amd64, arm64 := false, false, false
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		matches := func(goos, arch string) (bool, error) {
			target := build.Default
			target.GOOS, target.GOARCH, target.CgoEnabled = goos, arch, false
			return target.MatchFile(dir, name)
		}
		amd, err := matches("windows", "amd64")
		if err != nil {
			return nil, err
		}
		arm, err := matches("windows", "arm64")
		if err != nil {
			return nil, err
		}
		source, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		specific := strings.Contains(strings.TrimSuffix(name, ".go")+"_", "_windows_")
		for _, line := range strings.Split(string(source), "\n") {
			if strings.HasPrefix(line, "package ") {
				break
			}
			if constraint.IsGoBuild(line) || constraint.IsPlusBuild(line) {
				expr, err := constraint.Parse(line)
				if err != nil {
					return nil, err
				}
				specific = specific || positiveWindowsTag(expr, false)
			}
		}
		if specific && (amd || arm) {
			windows = true
		}
		amd64 = amd64 || amd
		if arm && !amd {
			arm64 = true
		}
	}
	if !windows {
		return nil, nil
	}
	var arches []string
	if amd64 {
		arches = append(arches, "amd64")
	}
	if arm64 {
		arches = append(arches, "arm64")
	}
	return arches, nil
}

func runWindowsCompileTargets(ctx context.Context, targets map[string][]string) error {
	return withGoTestScratch(func(output string) error {
		for _, module := range mapKeys(targets) {
			dir, err := filepath.Abs(filepath.Join(regexCheckRoot(), module))
			if err != nil {
				return err
			}
			for _, pkg := range targets[module] {
				arches, err := windowsCompileArchitectures(filepath.Join(dir, pkg))
				if err != nil {
					return fmt.Errorf("windows source selection %s/%s: %w", module, pkg, err)
				}
				for _, arch := range arches {
					binary := filepath.Join(output, "windows-"+arch+".test.exe")
					script := fmt.Sprintf("env -C %s %s", shellQuote(dir), boundedGoCommand(currentHost(), "test", "-c -o "+shellQuote(binary)+" "+shellQuote(pkg)))
					sparkwing.Info(ctx, "Windows compile: %s/%s windows/%s (compile only)", module, pkg, arch)
					if _, err := sparkwing.Bash(ctx, script).Env("GOWORK", "auto").Env("GOOS", "windows").Env("GOARCH", arch).Env("CGO_ENABLED", "0").Run(); err != nil {
						return fmt.Errorf("windows test compile %s/%s windows/%s: %w", module, pkg, arch, err)
					}
				}
			}
		}
		return nil
	})
}

func positiveWindowsTag(expr constraint.Expr, negated bool) bool {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return e.Tag == "windows" && !negated
	case *constraint.NotExpr:
		return positiveWindowsTag(e.X, !negated)
	case *constraint.AndExpr:
		return positiveWindowsTag(e.X, negated) || positiveWindowsTag(e.Y, negated)
	case *constraint.OrExpr:
		return positiveWindowsTag(e.X, negated) || positiveWindowsTag(e.Y, negated)
	}
	return false
}

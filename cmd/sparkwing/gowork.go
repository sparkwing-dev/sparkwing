package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

func goWorkInScope(startDir string) (string, bool) {
	switch env := os.Getenv("GOWORK"); env {
	case "off":
		return "", false
	case "":
	default:
		// #nosec G703 -- the path comes from this user's own environment
		if fi, err := os.Stat(env); err == nil && fi.Mode().IsRegular() {
			return env, true
		}
		return "", false
	}
	dir := startDir
	for {
		candidate := filepath.Join(dir, "go.work")
		if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func goWorkCovers(workPath, moduleDir string) bool {
	raw, err := os.ReadFile(workPath)
	if err != nil {
		return false
	}
	wf, err := modfile.ParseWork(workPath, raw, nil)
	if err != nil {
		return false
	}
	absModule, err := filepath.Abs(moduleDir)
	if err != nil {
		return false
	}
	workDir := filepath.Dir(workPath)
	for _, u := range wf.Use {
		p := u.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(workDir, p)
		}
		ap, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if filepath.Clean(ap) == filepath.Clean(absModule) {
			return true
		}
	}
	return false
}

func goModuleRoot(startDir string) (string, bool) {
	dir := startDir
	for {
		if fi, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && fi.Mode().IsRegular() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func goWorkNote(startDir string) string {
	root, ok := goModuleRoot(startDir)
	if !ok {
		return ""
	}
	work, present := goWorkInScope(root)
	if !present || goWorkCovers(work, root) {
		return ""
	}
	return fmt.Sprintf(
		"`%s` does not list this module, so `go build`, `go test` and `go run` here fail with "+
			"\"does not contain modules listed in go.work\". Run them as "+
			"`GOWORK=off go -C %s build ./...` -- `-C` on its own still fails, because go reads "+
			"the workspace from the directory it lands in, so `GOWORK=off` has to travel with it.",
		work, root,
	)
}

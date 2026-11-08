package procenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// LookupCommand resolves argv through the requesting process's PATH and working directory.
func LookupCommand(name string, env []string, dir string) (string, error) {
	windows := runtime.GOOS == "windows"
	if !windows && strings.Contains(name, string(filepath.Separator)) {
		return name, nil
	}
	path, set := "", false
	extensions := []string{""}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == "PATH" || windows && strings.EqualFold(key, "PATH") {
			path, set = value, true
		}
		if windows && strings.EqualFold(key, "PATHEXT") && filepath.Ext(name) == "" {
			extensions = append(extensions, strings.Split(value, ";")...)
		}
	}
	if !set {
		if windows {
			path = os.Getenv("PATH")
		} else {
			path = defaultSearchPath
		}
	}
	if windows && len(extensions) == 1 && filepath.Ext(name) == "" {
		extensions = append(extensions, ".com", ".exe", ".bat", ".cmd")
	}
	entries := filepath.SplitList(path)
	if path == "" {
		entries = []string{""}
	}
	if filepath.IsAbs(name) || windows && strings.ContainsAny(name, `/\`) {
		entries = []string{""}
	}
	for _, entry := range entries {
		if entry == "" {
			entry = "."
		}
		candidate := filepath.Join(entry, name)
		if filepath.IsAbs(name) {
			candidate = name
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(dir, candidate)
		}
		for _, extension := range extensions {
			file := candidate + extension
			if info, err := os.Stat(file); err == nil && !info.IsDir() && (windows || info.Mode()&0o111 != 0) {
				return file, nil
			}
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// hack: glibc's _CS_PATH, which execvp searches when PATH is unset.
const defaultSearchPath = "/bin:/usr/bin"

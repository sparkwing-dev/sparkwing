package procenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// LookupCommand resolves name using env and dir. An unset PATH uses /bin:/usr/bin
// on Unix and the host PATH on Windows. Windows also searches dir and applies PATHEXT.
func LookupCommand(name string, env []string, dir string) (string, error) {
	windows := runtime.GOOS == "windows"
	if !windows && strings.Contains(name, string(filepath.Separator)) {
		return name, nil
	}
	path, set := "", false
	extensions := []string{""}
	pathExt := ""
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == "PATH" || windows && strings.EqualFold(key, "PATH") {
			path, set = value, true
		}
		if windows && strings.EqualFold(key, "PATHEXT") {
			pathExt = value
		}
	}
	if !set {
		if windows {
			path = os.Getenv("PATH")
		} else {
			path = defaultSearchPath
		}
	}
	if windows && filepath.Ext(name) == "" {
		if pathExt == "" {
			pathExt = ".com;.exe;.bat;.cmd"
		}
		extensions = nil
		for _, extension := range strings.Split(pathExt, ";") {
			if extension != "" {
				if extension[0] != '.' {
					extension = "." + extension
				}
				extensions = append(extensions, extension)
			}
		}
	}
	entries := filepath.SplitList(path)
	if path == "" {
		entries = []string{""}
	}
	if windows {
		entries = append([]string{""}, entries...)
		if strings.ContainsAny(name, `:\/`) {
			entries = []string{""}
		}
	}
	if filepath.IsAbs(name) {
		entries = []string{""}
	}
	for _, entry := range entries {
		if entry == "" {
			entry = "."
		}
		candidate := filepath.Join(entry, name)
		if filepath.IsAbs(name) || windows && strings.ContainsAny(name, `:\/`) {
			candidate = name
		}
		if windows && !filepath.IsAbs(candidate) {
			volume := filepath.VolumeName(candidate)
			switch {
			case volume != "" && strings.EqualFold(volume, filepath.VolumeName(dir)):
				candidate = filepath.Join(dir, candidate[len(volume):])
			case volume != "":
				absolute, err := filepath.Abs(candidate)
				if err != nil {
					continue
				}
				candidate = absolute
			case strings.HasPrefix(candidate, `\`) || strings.HasPrefix(candidate, "/"):
				candidate = filepath.VolumeName(dir) + candidate
			}
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(dir, candidate)
		}
		for _, extension := range extensions {
			file := candidate + extension
			// #nosec G703 -- executable lookup must stat paths selected by the requesting process PATH.
			if info, err := os.Stat(file); err == nil && !info.IsDir() && (windows || info.Mode()&0o111 != 0) {
				return file, nil
			}
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// hack: glibc's _CS_PATH, which execvp searches when PATH is unset.
const defaultSearchPath = "/bin:/usr/bin"

package installsite

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func ExeName() string {
	if runtime.GOOS == "windows" {
		return "sparkwing.exe"
	}
	return "sparkwing"
}

type Copy struct {
	Path string `json:"path"`

	Resolved string `json:"resolved,omitempty"`

	ModTime time.Time `json:"mod_time"`
}

func Self() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return normalize(exe), nil
}

func PathKey(resolved string) string {
	sum := sha256.Sum256([]byte(resolved))
	return hex.EncodeToString(sum[:8])
}

type Remedy struct {
	Action     string
	Undo       string
	Executable bool
}

func (r Remedy) Text() string {
	if r.Executable {
		return r.Action + "   (undo: " + r.Undo + ")"
	}
	return r.Action + "; " + r.Undo
}

func RetireRemedy(path string) Remedy {
	return retireRemedy(runtime.GOOS, path)
}

func retireRemedy(goos, path string) Remedy {
	destination := path + ".superseded"
	if goos == "windows" {
		return Remedy{
			Action: fmt.Sprintf(
				"manual rename required: rename [%s] to [%s] with File Explorer or a command quoted for your chosen shell (cmd.exe and PowerShell require different escaping)",
				path, destination),
			Undo: fmt.Sprintf("undo manually by renaming [%s] back to [%s]", destination, path),
		}
	}

	quotedPath := shellQuote(path)
	quotedDestination := shellQuote(destination)
	return Remedy{
		Action: "test ! -e " + quotedDestination + " && test ! -L " + quotedDestination +
			" && mv -n -- " + quotedPath + " " + quotedDestination +
			" && test ! -e " + quotedPath + " && test ! -L " + quotedPath,
		Undo: "test ! -e " + quotedPath + " && test ! -L " + quotedPath +
			" && mv -n -- " + quotedDestination + " " + quotedPath +
			" && test ! -e " + quotedDestination + " && test ! -L " + quotedDestination,
		Executable: true,
	}
}

func shellQuote(path string) string {
	if path != "" && !strings.ContainsFunc(path, unixShellUnsafe) {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

func unixShellUnsafe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	switch r {
	case '@', '%', '+', '=', ':', ',', '.', '/', '-', '_':
		return false
	}
	return true
}

func SamePath(a, b string) bool {
	if a == b {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

func SearchDirs(getenv func(string) string, home string) []string {
	var dirs []string
	add := func(d string) {
		if d != "" {
			dirs = append(dirs, filepath.Clean(d))
		}
	}
	for _, d := range filepath.SplitList(getenv("PATH")) {
		add(d)
	}
	if home != "" {
		add(filepath.Join(home, ".local", "bin"))
		add(filepath.Join(home, "go", "bin"))
	}
	add(getenv("GOBIN"))
	if gopath := getenv("GOPATH"); gopath != "" {
		for _, d := range filepath.SplitList(gopath) {
			add(filepath.Join(d, "bin"))
		}
	}
	if runtime.GOOS == "windows" {
		if la := getenv("LOCALAPPDATA"); la != "" {
			add(filepath.Join(la, "sparkwing", "bin"))
		}
	} else {
		add("/usr/local/bin")
		add("/opt/homebrew/bin")
	}
	return dedupe(dirs)
}

func Scan(dirs []string) []Copy {
	var out []Copy
	seen := map[string]bool{}
	for _, dir := range dirs {
		p := filepath.Join(dir, ExeName())
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || !executable(fi) {
			continue
		}
		resolved := normalize(p)
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		out = append(out, Copy{Path: p, Resolved: resolved, ModTime: fi.ModTime()})
	}
	return out
}

func Competing(copies []Copy, self string) []Copy {
	var out []Copy
	for _, c := range copies {
		if self != "" && SamePath(c.Resolved, self) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func normalize(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

func executable(fi os.FileInfo) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return fi.Mode().Perm()&0o111 != 0
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

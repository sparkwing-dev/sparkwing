// Package installsite answers one read-only question: which sparkwing
// binaries are reachable on this machine, and which one is this?
//
// A laptop accumulates copies. `go install` drops one in GOBIN, the
// source installer builds another into ~/.local/bin, a package manager
// may leave a third in /usr/local/bin. That is tolerable until the
// copies disagree: PATH is not one list, so an interactive shell
// resolves the copy its PATH orders first while a launchd job, cron
// entry, or systemd unit resolves whatever its own PATH orders first,
// and the two can be different builds of the same command.
//
// This package only detects and identifies. It never records which
// install is the "right" one, never renames or removes a copy, and
// never compares version strings from binaries it did not build --
// a second binary is a file somebody installed on purpose, and which
// one to keep is the operator's choice. Callers (doctor, info, update,
// the source installer) report what the scan finds alongside the exact
// remedy, and stop there.
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

// ExeName is the file name a sparkwing install has on this platform.
func ExeName() string {
	if runtime.GOOS == "windows" {
		return "sparkwing.exe"
	}
	return "sparkwing"
}

// Copy is one sparkwing binary found on the search path.
type Copy struct {
	// Path is where the scan found it, before symlinks are followed.
	// This is the name an operator would type into a remedy.
	Path string `json:"path"`
	// Resolved is Path with symlinks followed. Two entries with the
	// same Resolved are one install reachable by two names, not two
	// competing installs, so identity is decided here and never on
	// Path.
	Resolved string `json:"resolved,omitempty"`
	// ModTime is the binary's modification time. It is reported rather
	// than acted on: it is the cheapest honest signal of which copy is
	// stale, and reading it costs no subprocess. Executing a foreign
	// binary to ask its version would be the alternative, and running
	// an unknown build to decide whether to trust it is backwards.
	ModTime time.Time `json:"mod_time"`
}

// Self is the absolute, symlink-resolved path of the running binary.
func Self() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return normalize(exe), nil
}

// PathKey is a short stable digest of a resolved executable path, used
// to key per-install state files. Feeding it a path [Self] or [Scan]
// resolved means two names for one binary -- a symlink, or macOS's
// /tmp alias of /private/tmp -- digest identically, while two distinct
// installs never share a key. The digest also flattens an arbitrary
// absolute path into one safe file name.
func PathKey(resolved string) string {
	sum := sha256.Sum256([]byte(resolved))
	return hex.EncodeToString(sum[:8])
}

// Remedy is read-only guidance for retiring one competing install. On Unix,
// Action and Undo are copy-pasteable shell commands. On Windows they are
// deliberately prose: cmd.exe and PowerShell escape legal filename characters
// differently, so pretending one command is safe in both shells is worse than
// asking the operator to use File Explorer or quote for the shell they chose.
type Remedy struct {
	Action     string
	Undo       string
	Executable bool
}

// Text renders a complete reversible remedy for a human-facing report.
func (r Remedy) Text() string {
	if r.Executable {
		return r.Action + "   (undo: " + r.Undo + ")"
	}
	return r.Action + "; " + r.Undo
}

// RetireRemedy returns non-destructive guidance for moving path aside. Unix
// commands refuse to overwrite either the retirement destination or, on undo,
// a newly recreated source. Windows guidance is non-executable and names both
// paths exactly.
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

// shellQuote renders one POSIX shell word. A path that needs no quoting is
// returned unchanged, so the ordinary remedy remains readable.
func shellQuote(path string) string {
	if path != "" && !strings.ContainsFunc(path, unixShellUnsafe) {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// unixShellUnsafe mirrors the character class Python's shlex.quote
// leaves bare: anything outside it makes the whole word get quoted.
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

// SamePath reports whether two paths name the same file. It asks the
// filesystem first, so a hard link or a differently-spelled path to one
// binary is not mistaken for two, and falls back to string comparison
// when either side cannot be stat'd.
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

// SearchDirs is where a sparkwing install can plausibly be found: every
// directory on the caller's PATH, then the well-known install targets.
//
// The well-known set is searched in addition to PATH because the whole
// problem is that PATH is not one list. The shell that runs the scan
// and the launchd job that resolves the rival do not share a PATH, so a
// scan that trusted only the caller's own PATH would report a clean
// machine from the very shell whose neighbor is the conflict.
//
// getenv is injected so a test can describe a machine it is not
// running on.
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

// Scan returns the sparkwing binaries in dirs, one entry per distinct
// install: two directories that reach the same file through a symlink
// collapse into the entry found first, because the second is a name for
// the install rather than a rival to it.
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

// Competing returns the scanned copies that are not the binary at self.
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

// executable reports whether fi is runnable. Windows has no execute
// bit, so the file name -- which the scan already fixed to sparkwing.exe
// -- is the whole test there.
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

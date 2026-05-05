// Package color provides ANSI color helpers for pipeline output.
//
//	fmt.Println(color.Green("deployed %s", version))
//	fmt.Println(color.Dim("skipping %s", name))
//	fmt.Printf("status: %s %s\n", color.Bold("PASS"), color.Dim(duration))
//
// Color emission auto-detects: enabled only when stdout is a TTY and
// neither NO_COLOR nor CI is set. Agents (Claude Code, Cursor, etc.)
// and pipes get plain text. CLICOLOR_FORCE=1 / SPARKWING_FORCE_COLOR=1
// re-enables for the rare case the user wants color through a pipe.
package color

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"golang.org/x/term"
)

// enabled is computed once at process start. Pure functions of env +
// the original stdout fd, so the result is stable for the lifetime of
// the process.
var enabled = detectEnabled()

func detectEnabled() bool {
	// Force-on overrides everything; useful for rare "I'm piping but
	// I want colors" cases (e.g. `sparkwing ... | less -R`).
	if os.Getenv("CLICOLOR_FORCE") == "1" || os.Getenv("SPARKWING_FORCE_COLOR") == "1" {
		return true
	}
	// no-color.org standard: any non-empty NO_COLOR disables.
	if v, ok := os.LookupEnv("NO_COLOR"); ok && v != "" {
		return false
	}
	// CI / agent runners: usually log-only, no terminal.
	if os.Getenv("CI") != "" {
		return false
	}
	return IsInteractiveStdout()
}

// IsInteractiveStdout reports whether stdout looks like an interactive
// terminal. Wraps golang.org/x/term.IsTerminal with a Windows-specific
// fallback for Git Bash, MSYS2, Cygwin, and similar frontends that
// use mintty (or another non-Console pty layer) over a pipe to the
// underlying process: term.IsTerminal's Console-mode check returns
// false there even when the user is typing into a real interactive
// shell. We accept any of:
//
//   - MSYSTEM set       -- Git Bash sets MINGW64, MSYS2 sets MSYS, etc.
//   - TERM_PROGRAM=mintty -- set by Git Bash even when MSYSTEM isn't.
//   - TERM contains "xterm" or "cygwin" -- catches stripped-down
//     Cygwin / MSYS environments that lose the brand-name vars but
//     still report a terminal-ish TERM.
//
// Use this anywhere you'd otherwise call term.IsTerminal on stdout
// (pkg/color, orchestrator format-selection, etc.) so every code path
// shares one definition of "interactive" and can't drift on what
// counts as "agent" vs. "human".
func IsInteractiveStdout() bool {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	if os.Getenv("MSYSTEM") != "" {
		return true
	}
	if os.Getenv("TERM_PROGRAM") == "mintty" {
		return true
	}
	switch t := os.Getenv("TERM"); {
	case t == "":
		return false
	case strings.Contains(t, "xterm"), strings.Contains(t, "cygwin"):
		return true
	}
	return false
}

// SetEnabled overrides the auto-detected setting. Mostly for tests
// and the rare downstream caller that wants explicit control.
func SetEnabled(on bool) { enabled = on }

// Enabled reports whether color output is currently emitted.
func Enabled() bool { return enabled }

func apply(code string, args ...any) string {
	if len(args) == 0 {
		return ""
	}
	text := fmt.Sprint(args[0])
	if len(args) > 1 {
		text = fmt.Sprintf(text, args[1:]...)
	}
	if !enabled {
		return text
	}
	return code + text + "\033[0m"
}

func Red(args ...any) string     { return apply("\033[31m", args...) }
func Green(args ...any) string   { return apply("\033[32m", args...) }
func Yellow(args ...any) string  { return apply("\033[33m", args...) }
func Blue(args ...any) string    { return apply("\033[34m", args...) }
func Magenta(args ...any) string { return apply("\033[35m", args...) }
func Cyan(args ...any) string    { return apply("\033[36m", args...) }
func Bold(args ...any) string    { return apply("\033[1m", args...) }
func Dim(args ...any) string     { return apply("\033[2m", args...) }

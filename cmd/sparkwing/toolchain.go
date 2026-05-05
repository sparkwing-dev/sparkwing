// Toolchain probes shared by `sparkwing pipeline new` and `sparkwing info`.
// The Go toolchain is required for the Go-pipeline compile path. These
// helpers exist so all callers print the same install hint in the same
// format -- a user who hits the warning twice should see the same words
// both times.
package main

import (
	"os/exec"
	"runtime"
	"strings"
)

// goOnPath reports whether the Go toolchain is reachable. Cheap;
// every command that needs it can call this without thinking about
// caching.
func goOnPath() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

// sparkwingOnPath reports whether `sparkwing` (the binary the user just
// ran) is reachable by name from a fresh shell. Used by the first-time
// card to decide whether to nag about PATH. exec.LookPath honors
// PATHEXT on Windows, so it finds sparkwing.exe via the bare name.
func sparkwingOnPath() bool {
	_, err := exec.LookPath("sparkwing")
	return err == nil
}

// userGoVersion returns the user's Go toolchain version (e.g.
// "go1.25.0") via `go env GOVERSION`. Empty string when Go isn't on
// PATH or the env probe fails. Used by `sparkwing pipeline new` to write a
// go.mod whose `go X.Y` directive matches what the user has, so
// modern Go's automatic toolchain switching is a no-op for the
// common case.
func userGoVersion() string {
	if !goOnPath() {
		return ""
	}
	out, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// userGoModDirective converts the toolchain version (e.g. "go1.25.0")
// into the major.minor form a go.mod's `go` directive expects (e.g.
// "1.25"). Empty string when Go isn't installed -- callers that need
// a fallback should provide their own.
func userGoModDirective() string {
	v := userGoVersion()
	if v == "" {
		return ""
	}
	v = strings.TrimPrefix(v, "go")
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// goInstallHint returns a one-line install instruction tuned to the
// host OS, or "" when Go is already on PATH. Callers that want to
// surface the hint regardless of whether Go is installed should
// instead use goInstallHintForce -- this one returns "" so a caller
// can `if h := goInstallHint(); h != "" { print(h) }` cleanly.
func goInstallHint() string {
	if goOnPath() {
		return ""
	}
	return goInstallHintForce()
}

// goInstallHintForce always returns the OS-appropriate install line.
// Used when the caller wants to print the hint as a "you'll need
// this if you write Go-based pipelines" footnote even on a machine
// that already has Go.
func goInstallHintForce() string {
	switch runtime.GOOS {
	case "darwin":
		return "Install Go: `brew install go` (or download from https://go.dev/dl)"
	case "linux":
		return "Install Go: `apt install golang-go` / `dnf install golang` / `pacman -S go` (or download from https://go.dev/dl)"
	default:
		return "Install Go: https://go.dev/dl"
	}
}

package main

import (
	"os/exec"
	"runtime"
	"strings"
)

func goOnPath() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

func sparkwingOnPath() bool {
	_, err := exec.LookPath("sparkwing")
	return err == nil
}

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

func goInstallHint() string {
	if goOnPath() {
		return ""
	}
	return goInstallHintForce()
}

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

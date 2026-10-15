package npmcmd

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Command resolves npm without a shell; the caller supplies its environment and cancellation.
func Command(args ...string) (*exec.Cmd, error) {
	return ForPlatform(runtime.GOOS, exec.LookPath, args...)
}

// ForPlatform uses the caller's executable lookup without invoking a command shell.
func ForPlatform(goos string, lookPath func(string) (string, error), args ...string) (*exec.Cmd, error) {
	npm, err := lookPath("npm")
	if err != nil {
		return nil, err
	}
	if goos != "windows" || strings.EqualFold(filepath.Ext(npm), ".exe") {
		return exec.Command(npm, args...), nil
	}
	node, err := lookPath("node")
	if err != nil {
		return nil, err
	}
	cli := filepath.Join(filepath.Dir(npm), "node_modules", "npm", "bin", "npm-cli.js")
	return exec.Command(node, append([]string{cli}, args...)...), nil
}

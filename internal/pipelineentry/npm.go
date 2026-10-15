package pipelineentry

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

// NPMCommand resolves npm for goos and preserves args without a shell. Windows
// command scripts run through Node's npm-cli.js because CreateProcess cannot run them.
func NPMCommand(ctx context.Context, goos string, lookPath func(string) (string, error), args ...string) (*exec.Cmd, error) {
	npm, err := lookPath("npm")
	if err != nil {
		return nil, err
	}
	if goos != "windows" || strings.EqualFold(filepath.Ext(npm), ".exe") {
		return exec.CommandContext(ctx, npm, args...), nil
	}
	node, err := lookPath("node")
	if err != nil {
		return nil, err
	}
	cli := filepath.Join(filepath.Dir(npm), "node_modules", "npm", "bin", "npm-cli.js")
	return exec.CommandContext(ctx, node, append([]string{cli}, args...)...), nil
}

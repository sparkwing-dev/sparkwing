package unitfile

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// CheckSystemdSource refuses a loaded unit from a different directory before mutation by name.
func CheckSystemdSource(ctx context.Context, execute func(context.Context, string, ...string) (string, error), name, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if execute == nil {
		return fmt.Errorf("native unit: command executor is required")
	}
	out, err := execute(ctx, "systemctl", "--user", "show", "-p", "FragmentPath", "--value", name)
	if err != nil && ctx.Err() == nil && strings.Contains(out, name) && strings.Contains(out, "could not be found") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("native unit: inspect %s registration: %w: %s", name, err, strings.TrimSpace(out))
	}
	if loaded := strings.TrimSpace(out); loaded != "" && !SameSource(loaded, path) {
		return fmt.Errorf("native unit %s is loaded from %s, not %s", name, loaded, path)
	}
	return nil
}

// CheckAgentSource refuses an agent whose launchd registration names another plist.
func CheckAgentSource(output, path string) error {
	for _, line := range strings.Split(output, "\n") {
		if loaded, ok := strings.CutPrefix(strings.TrimSpace(line), "path = "); ok {
			if loaded == path {
				return nil
			}
			return fmt.Errorf("native agent is loaded from %s, not %s", loaded, path)
		}
	}
	return fmt.Errorf("native agent: launchctl did not report the registration's path")
}

// SameSource accepts the manager's symlink to an owned unit file.
func SameSource(loaded, path string) bool {
	if loaded == path {
		return true
	}
	a, err := os.Stat(loaded)
	if err != nil {
		return false
	}
	b, err := os.Stat(path)
	return err == nil && os.SameFile(a, b)
}

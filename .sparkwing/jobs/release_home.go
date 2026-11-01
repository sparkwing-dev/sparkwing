package jobs

import (
	"fmt"
	"path/filepath"
)

func requireReleaseHome(getenv func(string) string, userHomeDir func() (string, error)) error {
	home := getenv("SPARKWING_HOME")
	if home == "" {
		return fmt.Errorf("SPARKWING_HOME must name an isolated directory; a release run must not share the operational runs store; retry with HOME=\"$(mktemp -d)\" SPARKWING_HOME=\"$(mktemp -d)\" sparkwing run release --sw-dry-run")
	}
	userHome, err := userHomeDir()
	if err != nil {
		return fmt.Errorf("resolve the operational Sparkwing home: %w", err)
	}
	if filepath.Clean(home) == filepath.Join(userHome, ".sparkwing") {
		return fmt.Errorf("SPARKWING_HOME %q is the operational runs store; retry with HOME=\"$(mktemp -d)\" SPARKWING_HOME=\"$(mktemp -d)\" sparkwing run release --sw-dry-run", home)
	}
	return nil
}

package jobs

import (
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func requireReleaseHome(getenv func(string) string, userHomeDir func() (string, error)) error {
	home := getenv("SPARKWING_HOME")
	if home == "" {
		return fmt.Errorf("%w: SPARKWING_HOME must name an isolated directory; a release run must not share the operational runs store", sparkwing.ErrIsolatedHomeRequired)
	}
	userHome, err := userHomeDir()
	if err != nil {
		return fmt.Errorf("resolve the operational Sparkwing home: %w", err)
	}
	if filepath.Clean(home) == filepath.Join(userHome, ".sparkwing") {
		return fmt.Errorf("%w: SPARKWING_HOME %q is the operational runs store", sparkwing.ErrIsolatedHomeRequired, home)
	}
	return nil
}

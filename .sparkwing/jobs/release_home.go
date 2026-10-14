package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: every other release job needs this one, so a release started
// against the operational Sparkwing home stops before the cut checks, the
// changelog commit or the tag push run. Plan cannot read the environment, so
// the check is a job.
type requireIsolatedHomeJob struct{ sparkwing.Base }

func (j *requireIsolatedHomeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(ctx context.Context) error {
		if err := requireReleaseHome(os.Getenv, os.UserHomeDir); err != nil {
			return fmt.Errorf("release: %w", err)
		}
		sparkwing.Info(ctx, "SPARKWING_HOME is isolated from the operational runs store")
		return nil
	}).SafeWithoutDryRun()
	return nil, nil
}

func requireReleaseHome(getenv func(string) string, userHomeDir func() (string, error)) error {
	home := getenv("SPARKWING_HOME")
	if home == "" {
		return fmt.Errorf("SPARKWING_HOME must name an isolated directory; a release run must not share the operational runs store")
	}
	userHome, err := userHomeDir()
	if err != nil {
		return fmt.Errorf("resolve the operational Sparkwing home: %w", err)
	}
	if filepath.Clean(home) == filepath.Join(userHome, ".sparkwing") {
		return fmt.Errorf("SPARKWING_HOME %q is the operational runs store; choose an isolated release directory", home)
	}
	return nil
}

package cluster

import (
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

// sweepLeftoverDeployKeys removes the deploy keys a crashed runner of this
// user left in its key directories, so a crash mid-fetch does not leave a
// team's key behind for longer than the next start.
func sweepLeftoverDeployKeys(logger *slog.Logger) {
	n, err := bincache.SweepSSHKeyDirs()
	if n > 0 {
		logger.Info("removed deploy keys a crashed fetch left behind", "count", n)
	}
	if err != nil {
		logger.Warn("deploy key sweep", "err", err)
	}
}

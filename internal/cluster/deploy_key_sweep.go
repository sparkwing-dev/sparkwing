package cluster

import (
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

func sweepLeftoverDeployKeys(logger *slog.Logger) {
	n, err := bincache.SweepSSHKeyDirs()
	if n > 0 {
		logger.Info("removed deploy keys a crashed fetch left behind", "count", n)
	}
	if err != nil {
		logger.Warn("deploy key sweep", "err", err)
	}
}

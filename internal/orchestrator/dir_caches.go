package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func nodeDirCaches(n *sparkwing.JobNode) []depcache.Spec {
	return sparkwing.RuntimePlumbing.Fns.NodeDirCaches(n)
}

func restoreDirCaches(ctx context.Context, n *sparkwing.JobNode) []*depcache.Run {
	return depcache.RestoreAll(ctx, nodeDirCaches(n), n.ID(), sparkwing.WorkDir())
}

func saveDirCaches(ctx context.Context, runs []*depcache.Run, runErr error) {
	depcache.SaveAll(ctx, runs, runErr)
}

package orchestrator

import (
	"context"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func nodeDirCaches(n *sparkwing.JobNode) []depcache.Spec {
	return sparkwing.RuntimePlumbing.Fns.NodeDirCaches(n)
}

func restoreDirCaches(ctx context.Context, n *sparkwing.JobNode) []*depcache.Run {
	specs := nodeDirCaches(n)
	if len(specs) == 0 {
		return nil
	}
	workdir := sparkwing.WorkDir()
	if workdir == "" {
		if cwd, err := os.Getwd(); err == nil {
			workdir = cwd
		} else {
			workdir = "."
		}
	}
	runs := make([]*depcache.Run, len(specs))
	for i, spec := range specs {
		runs[i] = depcache.Restore(ctx, spec, n.ID(), workdir)
	}
	return runs
}

func saveDirCaches(ctx context.Context, runs []*depcache.Run, runErr error) {
	for _, r := range runs {
		r.Save(ctx, runErr)
	}
}

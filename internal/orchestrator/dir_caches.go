package orchestrator

import (
	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func nodeDirCaches(n *sparkwing.JobNode) []depcache.Spec {
	return sparkwing.RuntimePlumbing.Fns.NodeDirCaches(n)
}

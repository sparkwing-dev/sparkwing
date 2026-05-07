// sparkwing-runner is the cluster-side runner binary. It carries the
// cluster.Main() dispatch for the `runner`, `worker`, and `agent`
// subcommands (the warm-pool pod's entry point in production) plus
// the k8s client, Prometheus registry, and OTel SDK they need.
package main

import (
	"github.com/sparkwing-dev/sparkwing/v2/internal/cluster"
)

func main() {
	cluster.Main()
}

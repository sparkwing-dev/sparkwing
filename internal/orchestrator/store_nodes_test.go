package orchestrator_test

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func find(nodes []*store.Node, id string) *store.Node {
	for _, n := range nodes {
		if n.NodeID == id {
			return n
		}
	}
	return nil
}

func nodeIDs(nodes []*store.Node) string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.NodeID)
	}
	return fmt.Sprintf("%v", ids)
}

package runner

import (
	"errors"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func NodeTerminal(n *store.Node) bool {
	return n != nil && n.Status == "done" && n.Outcome != ""
}

func ResultFromNode(n *store.Node) Result {
	res := Result{Outcome: sparkwing.Outcome(n.Outcome)}
	if n.Error != "" {
		res.Err = errors.New(n.Error)
	}
	if len(n.Output) > 0 {
		res.Output = n.Output
	}
	return res
}

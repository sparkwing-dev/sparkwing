package runner

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Runner interface {
	RunNode(ctx context.Context, req Request) Result
}

type Request struct {
	RunID    string
	NodeID   string
	Pipeline string
	// Timeout bounds the node's whole run on its host, every attempt that
	// runs there included; zero is none.
	Timeout time.Duration
	// Cores and MemoryBytes are the node's resource pin; zero is unpinned.
	Cores       float64
	MemoryBytes int64
	// ClaimWait is how long the node may wait for a claim; zero is the default.
	ClaimWait time.Duration
}

type Result struct {
	Outcome sparkwing.Outcome
	Output  any
	Err     error
}

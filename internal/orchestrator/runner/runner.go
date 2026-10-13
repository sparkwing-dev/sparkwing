package runner

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Runner interface {
	RunNode(ctx context.Context, req Request) Result
}

type LabelAdvertiser interface {
	AdvertisedLabels() []string
}

type Request struct {
	RunID    string
	NodeID   string
	Pipeline string
	Args     map[string]string
	Git      *sparkwing.Git
	Trigger  sparkwing.TriggerInfo

	Node *sparkwing.JobNode
	// Timeout is the node's declared wall-clock timeout; zero is none.
	Timeout time.Duration
	// Cores and MemoryBytes are the node's resource pin; zero is unpinned.
	Cores       float64
	MemoryBytes int64
	// ClaimWait is how long the node may wait for a claim; zero is the default.
	ClaimWait time.Duration

	Delegate sparkwing.Logger

	ReleaseWorkerSlot   func()
	ReacquireWorkerSlot func() bool
}

type Result struct {
	Outcome sparkwing.Outcome
	Output  any
	Err     error

	Usage *ResourceUsage
}

type ResourceUsage struct {
	CPUTime time.Duration

	MaxRSSBytes int64

	Wall time.Duration
}

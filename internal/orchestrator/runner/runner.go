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

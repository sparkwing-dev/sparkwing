package nodehost

import (
	"context"
	"time"
)

const (
	levelInfo = "info"
)

var reapTimeout = 10 * time.Second

type sessionProcess interface {
	LeaderExited() <-chan struct{}
	Kill() error
	Finish(context.Context, time.Duration) error
	Terminate(context.Context, time.Duration) error
}

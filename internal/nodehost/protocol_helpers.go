package nodehost

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

const (
	LevelInfo   = "info"
	LevelWarn   = "warn"
	maxTextLine = 1 << 20
)

var reapTimeout = 10 * time.Second

type sessionProcess interface {
	LeaderExited() <-chan struct{}
	Kill() error
	Finish(context.Context, time.Duration) error
	Terminate(context.Context, time.Duration) error
}

func invalid(format string, args ...any) error {
	return &apierr.Error{Code: apierr.PlanInvalid, Message: fmt.Sprintf(format, args...)}
}

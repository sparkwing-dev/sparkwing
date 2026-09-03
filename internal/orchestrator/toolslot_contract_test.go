package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var _ func(*NodeExecutor, context.Context, string, string, sparkwing.Logger, string) = (*NodeExecutor).emitToolSlotLog

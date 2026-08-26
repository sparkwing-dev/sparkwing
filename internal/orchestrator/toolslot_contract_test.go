package orchestrator

import "github.com/sparkwing-dev/sparkwing/sparkwing"

var _ func(*NodeExecutor, string, string, sparkwing.Logger, string) = (*NodeExecutor).emitToolSlotLog

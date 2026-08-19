package orchestrator

import "github.com/sparkwing-dev/sparkwing/sparkwing"

var _ func(*InProcessRunner, string, string, sparkwing.Logger, string) = (*InProcessRunner).emitToolSlotLog

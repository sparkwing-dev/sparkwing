package store

import "errors"

// PlanNodeID identifies the legacy planning node.
const PlanNodeID = "plan"

// CompileLogNode identifies a trigger's compilation log.
const CompileLogNode = "_compile"

// MaxPlanBytes bounds legacy plan snapshots.
const MaxPlanBytes = 4 << 20

// ErrPlanAccepted refuses replacement of an accepted plan snapshot.
var ErrPlanAccepted = errors.New("store: the run's plan was accepted from its planning claim")

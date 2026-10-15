package nodehost

import (
	"time"
)

const defaultSkipBudget = 30 * time.Second

// safety: bounds the after_run eval process of a timed-out attempt, whose own budget is spent.
const afterRunTimeoutLimit = 2 * time.Minute

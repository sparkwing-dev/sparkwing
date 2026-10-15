package nodehost

import (
	"time"
)

// safety: matches the dispatcher's bound on a cache key resolver.
const memoKeyBudget = 30 * time.Second

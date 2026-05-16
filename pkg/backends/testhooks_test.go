package backends

import "sync"

func resetShimWarnedForTest() { shimWarned = sync.Once{} }

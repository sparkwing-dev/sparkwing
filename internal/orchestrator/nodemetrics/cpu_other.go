//go:build !unix

package nodemetrics

import "time"

func readCPUTime() (time.Duration, bool) { return 0, false }

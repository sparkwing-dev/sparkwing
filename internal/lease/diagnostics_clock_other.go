//go:build !linux

package lease

func monotonicClock() int64 { return 0 }

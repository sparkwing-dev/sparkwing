//go:build !linux && !darwin

package nodemetrics

func processRSS() (int64, bool) { return 0, false }

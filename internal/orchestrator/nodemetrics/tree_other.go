//go:build !linux && !darwin

package nodemetrics

func processSnapshot(int) (map[int]processSample, bool) { return nil, false }

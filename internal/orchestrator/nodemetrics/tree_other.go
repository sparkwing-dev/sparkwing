//go:build !linux && !darwin

package nodemetrics

func snapshotProcesses(func(map[int]processSample) map[int]processSample) (map[int]processSample, bool) {
	return nil, false
}

func sessionMembers(int, map[int]processSample) map[int]processSample { return nil }

//go:build unix && !darwin

package sparkwing

func maxRSSToBytes(maxrss int64) int64 {
	if maxrss < 0 {
		return 0
	}
	return maxrss * 1024
}

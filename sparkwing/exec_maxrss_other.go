//go:build unix && !darwin

package sparkwing

// maxRSSToBytes converts a wait4 ru_maxrss into bytes. Linux and the BSDs
// report ru_maxrss in kilobytes.
func maxRSSToBytes(maxrss int64) int64 {
	if maxrss < 0 {
		return 0
	}
	return maxrss * 1024
}

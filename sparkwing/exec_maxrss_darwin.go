//go:build darwin

package sparkwing

// maxRSSToBytes converts a wait4 ru_maxrss into bytes. Darwin reports
// ru_maxrss in bytes.
func maxRSSToBytes(maxrss int64) int64 {
	if maxrss < 0 {
		return 0
	}
	return maxrss
}

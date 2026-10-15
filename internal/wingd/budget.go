package wingd

import "fmt"

func humanBytesLog(v uint64) string {
	const unit = 1024.0
	f := float64(v)
	if f < unit {
		return fmt.Sprintf("%dB", v)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	n := f
	i := -1
	for n >= unit && i < len(units)-1 {
		n /= unit
		i++
	}
	return fmt.Sprintf("%.1f%s", n, units[i])
}

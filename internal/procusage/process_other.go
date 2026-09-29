//go:build !darwin && !linux

package procusage

import "time"

func (*Tree) Read() Snapshot {
	now := time.Now()
	return Snapshot{Start: now, End: now}
}

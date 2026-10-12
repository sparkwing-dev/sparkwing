//go:build linux || darwin

package nodemetrics

import "golang.org/x/sys/unix"

func sessionMembers(sid int, processes map[int]processSample) map[int]processSample {
	members := make(map[int]processSample)
	for pid, p := range processes {
		// safety: a process that exits between the listing and getsid fails the
		// lookup and drops out, as it would from the next listing.
		if got, err := unix.Getsid(pid); err == nil && got == sid {
			members[pid] = p
		}
	}
	return members
}

package wingd

import "os"

// LogCapBytes is the size past which the daemon log is rotated once, to
// d.log.1, so a long-lived home cannot grow it without bound. One
// rotation keeps the previous stretch's tail for a post-mortem and
// bounds the pair at roughly twice the cap.
const LogCapBytes = 1 << 20

// RotateLogOverCap renames home's daemon log to d.log.1 when it has
// grown past [LogCapBytes], and reports whether it did. A missing log is
// not an error: there is nothing to rotate and nothing to report.
//
// Both the spawning client and the running daemon rotate, and they have
// to agree on the cap and on the once-rotated .1 shape, so the rule
// lives here rather than at either call site. The client rotates because
// it is what opens the log for a detached spawn; the daemon rotates
// because a SIGUSR1 dump appends up to 2MB to a process that may not
// restart for weeks, which the spawn-time check never sees.
func RotateLogOverCap(home string) (bool, error) {
	path, err := LogPath(home)
	if err != nil {
		return false, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false, nil
	}
	if fi.Size() <= LogCapBytes {
		return false, nil
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return false, err
	}
	return true, nil
}

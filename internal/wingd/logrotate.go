package wingd

import (
	"io"
	"os"
)

// LogCapBytes is the size past which the daemon log is rotated once, to
// d.log.1, so a long-lived home cannot grow it without bound. One
// rotation keeps the previous stretch's tail for a post-mortem and
// bounds the pair at roughly twice the cap.
const LogCapBytes = 1 << 20

// RotateLogOverCap archives home's daemon log to d.log.1 and empties it
// when it has grown past [LogCapBytes], and reports whether it did. A
// missing log is not an error: there is nothing to rotate and nothing to
// report.
//
// It copies the contents aside and truncates the original in place
// rather than renaming it, because the log is not a file its writers
// reopen -- it is a descriptor they inherited, and there are three of
// them. The client points the supervisor's stdout and stderr at d.log
// before starting it, the supervisor hands those same descriptors to
// every daemon it starts, and it logs through them itself. A rename
// strands all of them on the archive: d.log would never grow again, so
// it would never rotate again, while d.log.1 grew without bound and a
// later rotation would unlink the inode they were still writing to.
// Truncating keeps one inode, so every holder follows the rotation
// without being told.
//
// Every writer opens the log O_APPEND, which recomputes the offset on
// each write, so they resume at the start of the emptied file rather
// than leaving a hole. A line written between the copy and the truncate
// is lost; that window is a few milliseconds against a log that only
// rotates once a megabyte, and losing a line of an operational log is
// the cheap failure here.
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
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return false, nil
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	if fi.Size() <= LogCapBytes {
		return false, nil
	}
	archive, err := os.OpenFile(path+".1", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(archive, f); err != nil {
		_ = archive.Close()
		return false, err
	}
	if err := archive.Close(); err != nil {
		return false, err
	}
	// safety: truncate only after the archive is closed clean, so a
	// failed copy never empties the only copy of the log.
	if err := f.Truncate(0); err != nil {
		return false, err
	}
	return true, nil
}

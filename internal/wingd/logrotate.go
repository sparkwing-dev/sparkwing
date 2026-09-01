package wingd

import (
	"io"
	"os"
)

const LogCapBytes = 1 << 20

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

//go:build windows

package wingd

import (
	"path/filepath"
	"strings"
)

func socketHomeIdentity(home string) (path, key string, err error) {
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", "", err
	}
	volume := filepath.VolumeName(abs)
	if len(volume) == 2 && volume[1] == ':' {
		abs = strings.ToUpper(volume) + abs[len(volume):]
	}
	path = filepath.Clean(abs)
	// safety: NTFS resolves any casing of the home to one directory and one
	// election lock, so every casing must select that lock's socket.
	return path, strings.ToUpper(path), nil
}

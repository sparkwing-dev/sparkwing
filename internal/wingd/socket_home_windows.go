//go:build windows

package wingd

import (
	"path/filepath"
	"strings"
)

func socketHomeIdentity(home string) (string, error) {
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	// bug: equivalent Windows spellings share an election lock and must select the same socket.
	volume := filepath.VolumeName(abs)
	if len(volume) == 2 && volume[1] == ':' {
		abs = strings.ToUpper(volume) + abs[len(volume):]
	}
	return filepath.Clean(abs), nil
}

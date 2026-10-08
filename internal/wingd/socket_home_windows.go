//go:build windows

package wingd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
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

func socketBaseDir() (string, error) {
	// safety: the socket path must be a pure function of the home so every
	// caller agrees on it whatever the environment. os.TempDir reads TMP, TEMP
	// and USERPROFILE, and the LocalAppData known folder expands USERPROFILE
	// even when given the token, so two processes sharing one election lock
	// could bind different sockets. The token's profile directory comes from
	// the account's profile record and reads no variables.
	profile, err := windows.GetCurrentProcessToken().GetUserProfileDirectory()
	if err != nil {
		return "", fmt.Errorf("wingd: resolve the user profile directory: %w", err)
	}
	base := filepath.Join(profile, "AppData", "Local", "Temp")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("wingd: prepare socket base directory %s: %w", base, err)
	}
	return base, nil
}

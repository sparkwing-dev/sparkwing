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
	// safety: every process sharing a home's election lock must bind one socket, so the base
	// reads no variables: os.TempDir follows TMP, TEMP and USERPROFILE, and the LocalAppData
	// known folder expands USERPROFILE even with a token. The token's profile directory comes
	// from the account's profile record.
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

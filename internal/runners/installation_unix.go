//go:build !windows

package runners

import "os"

func replaceInstalledRunner(source, target string) error {
	return os.Rename(source, target)
}

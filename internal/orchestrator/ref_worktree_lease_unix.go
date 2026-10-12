//go:build !windows

package orchestrator

import "os"

func openRefWorktreeLease(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

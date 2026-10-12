//go:build !windows

package orchestrator

import "os"

func heldRunStoreIdentity(path string) (os.FileInfo, error) { return os.Stat(path) }

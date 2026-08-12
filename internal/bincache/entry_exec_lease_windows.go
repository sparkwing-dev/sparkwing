//go:build windows

package bincache

import "os"

func prepareExecLease(_ *os.File, _ Entry, env []string) ([]string, func() error, error) {
	return env, func() error { return nil }, nil
}

// AdoptExecLeaseFromEnv is a no-op on Windows: ExecReplace keeps the lease in
// the waiting parent because Windows has no process-image replacement.
func AdoptExecLeaseFromEnv() error { return nil }

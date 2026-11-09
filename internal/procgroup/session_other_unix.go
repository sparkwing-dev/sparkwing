//go:build !windows && !linux && !darwin

package procgroup

import "errors"

var errGuardedSessionUnsupported = errors.New("durable process-session identity is unavailable on this platform")

func guardedSessionSupport() error { return errGuardedSessionUnsupported }

func sessionIdentity(int) (int, string, error) { return 0, "", errGuardedSessionUnsupported }

func signalGuardSession(int, bool) error { return errGuardedSessionUnsupported }

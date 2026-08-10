//go:build windows

package procgroup

func guardedSessionSupport() error { return errUnsupported }

func sessionIdentity(int) (int, string, error) { return 0, "", errUnsupported }

func terminateGuardSession(int) error { return errUnsupported }

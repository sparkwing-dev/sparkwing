//go:build !windows

package client

func socketDirMissingAfterDial(string, error) bool { return false }

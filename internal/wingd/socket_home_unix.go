//go:build !windows

package wingd

func socketHomeIdentity(home string) (string, error) { return home, nil }

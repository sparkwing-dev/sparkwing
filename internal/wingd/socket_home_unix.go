//go:build !windows

package wingd

func socketHomeIdentity(home string) (path, key string, err error) { return home, home, nil }

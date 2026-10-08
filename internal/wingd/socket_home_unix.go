//go:build !windows

package wingd

func socketHomeIdentity(home string) (path, key string, err error) { return home, home, nil }

func socketBaseDir() (string, error) {
	// safety: the socket path must be a pure function of the home so every
	// caller agrees on it whatever the environment. A short shared base keeps
	// the path inside the sun_path limit; checkSocketBase and checkSocketDir
	// carry the privacy that a per-user base would otherwise provide.
	return "/tmp", nil
}

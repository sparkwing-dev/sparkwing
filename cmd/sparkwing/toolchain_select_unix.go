//go:build !windows

package main

import "github.com/sparkwing-dev/sparkwing/internal/procenv"

func execToolchain(bin string, args, env []string) error {
	// #nosec G702 -- the engine resolver verified this executable and its release identity
	return procenv.Replace(bin, append([]string{bin}, args...), env)
}

//go:build !windows

package main

import "syscall"

func execToolchain(bin string, args, env []string) error {
	// #nosec G702 -- the engine resolver verified this executable and its release identity
	return syscall.Exec(bin, append([]string{bin}, args...), env)
}

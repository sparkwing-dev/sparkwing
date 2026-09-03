//go:build !windows

package main

import "syscall"

func execToolchain(bin string, args, env []string) error {
	// #nosec G702 -- a release binary whose digest the caller matched against the signed release manifest stored beside it
	return syscall.Exec(bin, append([]string{bin}, args...), env)
}

//go:build !windows

package main

import "syscall"

func execToolchain(bin string, args, env []string) error {
	// #nosec G702 -- a release binary this process just verified against its signed manifest digest
	return syscall.Exec(bin, append([]string{bin}, args...), env)
}

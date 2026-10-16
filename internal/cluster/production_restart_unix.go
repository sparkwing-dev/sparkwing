//go:build !windows

package cluster

import "syscall"

func restartRunner(executable string, args, environment []string) error {
	return syscall.Exec(executable, append([]string{executable}, args...), environment)
}

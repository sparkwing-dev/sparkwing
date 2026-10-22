//go:build unix

package procenv

import "syscall"

// Replace executes a new program in the current process with an explicit environment.
func Replace(name string, args, environment []string) error {
	return syscall.Exec(name, args, append([]string{}, environment...))
}

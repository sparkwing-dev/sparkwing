//go:build !windows

package cluster

import "github.com/sparkwing-dev/sparkwing/internal/procenv"

func restartRunner(executable string, args, environment []string) error {
	return procenv.Replace(executable, append([]string{executable}, args...), environment)
}

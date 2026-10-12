//go:build unix

package repos

import (
	"os"
	"os/exec"
)

func passPipes(cmd *exec.Cmd, requests, frames *os.File) error {
	cmd.ExtraFiles = []*os.File{requests, frames}
	return nil
}

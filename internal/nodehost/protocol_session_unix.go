//go:build unix

package nodehost

import (
	"os"
	"os/exec"
)

func startProtocolProcess(cmd *exec.Cmd, requests, frames *os.File) (sessionProcess, error) {
	return startProcess(cmd, requests, frames)
}

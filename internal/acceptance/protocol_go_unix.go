//go:build unix

package acceptance

import (
	"os"
	"os/exec"
)

func goSDKCommandPipes(command *exec.Cmd, request, frame *os.File) error {
	command.ExtraFiles = []*os.File{request, frame}
	return nil
}

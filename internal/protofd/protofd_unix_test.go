//go:build unix

package protofd_test

import (
	"os"
	"os/exec"
	"syscall"
)

func inheritPipes(command *exec.Cmd, request, frame *os.File) error {
	command.ExtraFiles = []*os.File{request, frame}
	return nil
}

func pipeInheritable(pipe *os.File) (bool, error) {
	flags, _, err := syscall.Syscall(syscall.SYS_FCNTL, pipe.Fd(), syscall.F_GETFD, 0)
	if err != 0 {
		return false, err
	}
	return flags&syscall.FD_CLOEXEC == 0, nil
}

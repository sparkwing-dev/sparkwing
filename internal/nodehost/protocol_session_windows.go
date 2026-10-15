//go:build windows

package nodehost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

func startProtocolProcess(cmd *exec.Cmd, requests, frames *os.File) (sessionProcess, error) {
	handles := []syscall.Handle{syscall.Handle(requests.Fd()), syscall.Handle(frames.Fd())}
	for _, handle := range handles {
		if err := windows.SetHandleInformation(windows.Handle(handle), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return nil, err
		}
		defer func() { _ = windows.SetHandleInformation(windows.Handle(handle), windows.HANDLE_FLAG_INHERIT, 0) }()
	}
	cmd.Args = append(cmd.Args, protofd.HandlesArg, fmt.Sprintf("%d,%d", handles[0], handles[1]))
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	job, err := procgroup.NewJob("")
	if err != nil {
		return nil, err
	}
	if err := job.Start(cmd); err != nil {
		if cmd.Process != nil {
			err = errors.Join(err, cmd.Process.Release())
		}
		return nil, errors.Join(err, job.Close())
	}
	process := &jobProcess{job: job, exited: make(chan struct{})}
	go func() {
		var exit *exec.ExitError
		if err := cmd.Wait(); !errors.As(err, &exit) {
			process.waitErr = err
		}
		close(process.exited)
	}()
	return process, nil
}

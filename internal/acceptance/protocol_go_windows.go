//go:build windows

package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/pkg/protofd"
)

func goSDKCommandPipes(command *exec.Cmd, request, frame *os.File) error {
	handles := []syscall.Handle{syscall.Handle(request.Fd()), syscall.Handle(frame.Fd())}
	for _, handle := range handles {
		if err := syscall.SetHandleInformation(handle, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
			return err
		}
	}
	command.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	command.Args = append(command.Args, protofd.HandlesArg, fmt.Sprintf("%d,%d", handles[0], handles[1]))
	return nil
}

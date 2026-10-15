//go:build windows

package protofd_test

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

func inheritPipes(command *exec.Cmd, request, frame *os.File) error {
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

func pipeInheritable(pipe *os.File) (bool, error) {
	var flags uint32
	get := syscall.NewLazyDLL("kernel32.dll").NewProc("GetHandleInformation")
	ok, _, err := get.Call(pipe.Fd(), uintptr(unsafe.Pointer(&flags)))
	if ok == 0 {
		return false, err
	}
	return flags&syscall.HANDLE_FLAG_INHERIT != 0, nil
}

package nativehost_test

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func attach(t *testing.T, command *exec.Cmd, request, frame *os.File) string {
	t.Helper()
	handles := []syscall.Handle{syscall.Handle(request.Fd()), syscall.Handle(frame.Fd())}
	for _, handle := range handles {
		if err := syscall.SetHandleInformation(handle, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
			t.Fatal(err)
		}
	}
	command.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	arg := fmt.Sprintf("%d,%d", handles[0], handles[1])
	command.Args = append(command.Args, "--sw-protocol-handles", arg)
	return arg
}

func checkClosed(t *testing.T, identities string) {
	t.Helper()
	for _, identity := range strings.Split(identities, ",") {
		raw, err := strconv.ParseUint(identity, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		const errorInvalidHandle = syscall.Errno(6)
		if _, err := syscall.GetFileType(syscall.Handle(raw)); err != errorInvalidHandle {
			t.Fatalf("child can access inherited protocol handle: %v", err)
		}
	}
}

package nativehost_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func attach(t *testing.T, command *exec.Cmd, request, frame *os.File) string {
	t.Helper()
	handles := []syscall.Handle{syscall.Handle(request.Fd()), syscall.Handle(frame.Fd())}
	var identities []string
	for _, handle := range handles {
		if err := syscall.SetHandleInformation(handle, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
			t.Fatal(err)
		}
		// safety: startSDK closes its originals before the probe, so retain a non-inherited witness.
		var witness windows.Handle
		if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(handle), windows.CurrentProcess(),
			&witness, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = windows.CloseHandle(witness) })
		identities = append(identities, fmt.Sprintf("%d:%d:%d", handle, os.Getpid(), witness))
	}
	command.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	arg := fmt.Sprintf("%d,%d", handles[0], handles[1])
	command.Args = append(command.Args, "--sw-protocol-handles", arg)
	return strings.Join(identities, ",")
}

func checkClosed(t *testing.T, identities string) {
	t.Helper()
	for _, identity := range strings.Split(identities, ",") {
		var raw, witness uintptr
		var pid uint32
		if _, err := fmt.Sscanf(identity, "%d:%d:%d", &raw, &pid, &witness); err != nil {
			t.Fatal(err)
		}
		// safety: freeze the candidate before importing the witness can reuse an absent handle number.
		var candidate windows.Handle
		err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(raw), windows.CurrentProcess(),
			&candidate, 0, false, windows.DUPLICATE_SAME_ACCESS)
		if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = windows.CloseHandle(candidate) })
		parent, err := windows.OpenProcess(windows.PROCESS_DUP_HANDLE, false, pid)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = windows.CloseHandle(parent) })
		var expected windows.Handle
		if err := windows.DuplicateHandle(parent, windows.Handle(witness), windows.CurrentProcess(),
			&expected, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = windows.CloseHandle(expected) })
		compare := windows.NewLazySystemDLL("kernelbase.dll").NewProc("CompareObjectHandles")
		if err := compare.Find(); err != nil {
			t.Fatal(err)
		}
		if same, _, _ := compare.Call(uintptr(candidate), uintptr(expected)); same != 0 {
			t.Fatal("child can access inherited protocol pipe object")
		}
	}
}

func inheritProbePipe(t *testing.T, command *exec.Cmd, pipe *os.File, identities string) string {
	t.Helper()
	for _, handle := range command.SysProcAttr.AdditionalInheritedHandles {
		if err := syscall.SetHandleInformation(handle, syscall.HANDLE_FLAG_INHERIT, 0); err != nil {
			t.Fatal(err)
		}
	}
	command.SysProcAttr.AdditionalInheritedHandles = nil
	if pipe != nil {
		handle := syscall.Handle(pipe.Fd())
		if err := syscall.SetHandleInformation(handle, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
			t.Fatal(err)
		}
		command.SysProcAttr.AdditionalInheritedHandles = []syscall.Handle{handle}
		first, rest, _ := strings.Cut(identities, ",")
		_, metadata, hasMetadata := strings.Cut(first, ":")
		first = fmt.Sprint(handle)
		if hasMetadata {
			first += ":" + metadata
		}
		identities = first + "," + rest
	}
	return identities
}

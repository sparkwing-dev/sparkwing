//go:build !windows

package nativehost_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func attach(t *testing.T, command *exec.Cmd, request, frame *os.File) string {
	t.Helper()
	command.ExtraFiles = []*os.File{request, frame}
	var identities []string
	for i, file := range command.ExtraFiles {
		var stat syscall.Stat_t
		if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
			t.Fatal(err)
		}
		identities = append(identities, fmt.Sprintf("%d:%d:%d", i+3, stat.Dev, stat.Ino))
	}
	return strings.Join(identities, ",")
}

func checkClosed(t *testing.T, identities string) {
	t.Helper()
	for _, identity := range strings.Split(identities, ",") {
		var fd int
		var dev, ino uint64
		if _, err := fmt.Sscanf(identity, "%d:%d:%d", &fd, &dev, &ino); err != nil {
			t.Fatal(err)
		}
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err == nil && uint64(stat.Dev) == dev && uint64(stat.Ino) == ino { //nolint:unconvert // Dev and Ino are narrower than uint64 on some Unix platforms
			t.Fatal("child inherited a protocol pipe")
		}
	}
}

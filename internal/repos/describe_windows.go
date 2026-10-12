//go:build windows

package repos

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

// hack: Windows has no descriptor numbering, so the pipes travel as inherited
// handles that protofd.HandlesEnv names.
func passPipes(cmd *exec.Cmd, requests, frames *os.File) error {
	handles := []syscall.Handle{syscall.Handle(requests.Fd()), syscall.Handle(frames.Fd())}
	for _, h := range handles {
		if err := syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
			return fmt.Errorf("inherit protocol pipe: %w", err)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("%s=%d,%d", protofd.HandlesEnv, handles[0], handles[1]))
	return nil
}

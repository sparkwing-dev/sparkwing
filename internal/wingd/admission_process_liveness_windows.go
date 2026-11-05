package wingd

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func admissionProcessRunning(pid int) error {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return fmt.Errorf("%w: process %d", procgroup.ErrProcessAbsent, pid)
		}
		return err
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, 0)
	if err != nil {
		return err
	}
	switch state {
	case windows.WAIT_OBJECT_0:
		return fmt.Errorf("%w: process %d exited", procgroup.ErrProcessAbsent, pid)
	case uint32(windows.WAIT_TIMEOUT):
		return nil
	default:
		return fmt.Errorf("process %d liveness wait returned %d", pid, state)
	}
}

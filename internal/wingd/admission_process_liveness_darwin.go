package wingd

import (
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func admissionProcessRunning(pid int) error {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return err
	}
	const zombieState = 5
	if len(processes) != 1 || int(processes[0].Proc.P_pid) != pid || processes[0].Proc.P_stat == zombieState {
		return fmt.Errorf("%w: process %d exited", procgroup.ErrProcessAbsent, pid)
	}
	return nil
}

package wingd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func admissionProcessRunning(pid int) error {
	return admissionProcessRunningRead(pid, os.ReadFile)
}

func admissionProcessRunningRead(pid int, readFile func(string) ([]byte, error)) error {
	data, err := readFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("%w: process %d", procgroup.ErrProcessAbsent, pid)
		}
		return err
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 {
		return fmt.Errorf("malformed process stat for %d", pid)
	}
	fields := strings.Fields(string(data[closeParen+1:]))
	if len(fields) == 0 {
		return fmt.Errorf("process state missing for %d", pid)
	}
	if fields[0] == "Z" || fields[0] == "X" || fields[0] == "x" {
		return fmt.Errorf("%w: process %d exited", procgroup.ErrProcessAbsent, pid)
	}
	return nil
}

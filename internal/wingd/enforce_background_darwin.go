//go:build darwin

package wingd

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	prioDarwinProcess = 4
	prioDarwinBG      = 0x1000
)

func backgroundProcess(pid int) error {
	var errs []error
	if err := unix.Setpriority(prioDarwinProcess, pid, prioDarwinBG); err != nil {
		errs = append(errs, fmt.Errorf("background qos: %w", err))
	}
	if err := unix.Setpriority(unix.PRIO_PROCESS, pid, backgroundNice); err != nil {
		errs = append(errs, fmt.Errorf("nice: %w", err))
	}
	return errors.Join(errs...)
}

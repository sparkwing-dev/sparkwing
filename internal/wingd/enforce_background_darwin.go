//go:build darwin

package wingd

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Darwin process-policy selectors and the background QoS value, absent
// from x/sys/unix. setpriority(PRIO_DARWIN_PROCESS, pid, PRIO_DARWIN_BG)
// is the taskpolicy(8) -b equivalent: the process is scheduled on
// efficiency cores with throttled I/O.
const (
	prioDarwinProcess = 4
	prioDarwinBG      = 0x1000
)

// backgroundProcess demotes an admitted run to background QoS and raises
// its scheduler nice. The QoS demotion does the real throttling; the nice
// bump makes the demotion visible to standard tools and adds ordinary
// scheduler yielding. Enforcement is advisory scheduling, not a hard cap.
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

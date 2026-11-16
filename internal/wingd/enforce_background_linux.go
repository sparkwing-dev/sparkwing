//go:build linux

package wingd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// safety: Linux niceness is per thread, so renicing only the leader would leave a Go holder's
// other threads, and the children they fork, at the default priority.
func backgroundProcess(pid int) error {
	tasks, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "task"))
	if err != nil {
		return fmt.Errorf("nice: %w", err)
	}
	var errs []error
	for _, task := range tasks {
		tid, err := strconv.Atoi(task.Name())
		if err != nil {
			continue
		}
		if err := unix.Setpriority(unix.PRIO_PROCESS, tid, backgroundNice); err != nil && !errors.Is(err, unix.ESRCH) {
			errs = append(errs, fmt.Errorf("nice thread %d: %w", tid, err))
		}
	}
	return errors.Join(errs...)
}

package wingd

import "github.com/sparkwing-dev/sparkwing/internal/procgroup"

func admissionProcessBirth(pid int) (string, error) {
	if err := admissionProcessRunning(pid); err != nil {
		return "", err
	}
	return procgroup.ProcessBirth(pid)
}

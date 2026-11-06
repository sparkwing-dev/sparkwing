package wingd

import (
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func admissionExecutionEmpty(pid int, birth string) (bool, error) {
	actual, err := admissionProcessBirth(pid)
	if errors.Is(err, procgroup.ErrProcessAbsent) {
		return true, nil
	}
	return err == nil && actual != birth, err
}

func killAdmissionExecution(pid int, birth string) error {
	empty, err := admissionExecutionEmpty(pid, birth)
	if err != nil || empty {
		return err
	}
	// safety: the runner owns the kill-on-close Job Object; retain capacity until its handle closes.
	return errors.New("waiting for the runner's Job Object to close")
}

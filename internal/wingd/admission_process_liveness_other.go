//go:build !linux && !darwin && !windows

package wingd

import "errors"

func admissionProcessRunning(int) error {
	return errors.New("process liveness inspection unsupported")
}

//go:build !linux && !darwin && !windows

package wingd

import (
	"errors"
	"os/exec"
)

func configureDetachedLocalRunner(*exec.Cmd) error {
	return errors.New("wingd: unmanaged runner detachment is unavailable on this operating system")
}

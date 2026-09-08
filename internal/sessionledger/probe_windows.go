//go:build windows

package sessionledger

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

type platformProbe struct{}

func (platformProbe) OwnerAlive(rec Record) (bool, error) {
	birth, err := procgroup.ProcessBirth(rec.OwnerPID)
	if errors.Is(err, procgroup.ErrProcessAbsent) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return birth == rec.OwnerBirth, nil
}

func (platformProbe) Terminate(_ context.Context, h Handle) error {
	if h.Kind != "job" || h.JobName == "" {
		return errors.New("sessionledger: handle kind " + h.Kind + " is not a Windows job")
	}
	return procgroup.TerminateJobByName(h.JobName)
}

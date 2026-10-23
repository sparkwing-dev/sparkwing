//go:build unix

package sessionledger

import (
	"context"
	"errors"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

type platformProbe struct{}

func (platformProbe) OwnerAlive(rec Record) (bool, error) {
	birth, err := procgroup.ProcessBirth(rec.OwnerPID)
	if errors.Is(err, procgroup.ErrProcessAbsent) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return birth == rec.OwnerBirth, nil
}

func (platformProbe) Terminate(ctx context.Context, h Handle) error {
	if h.Kind != "session" {
		return errors.New("sessionledger: handle kind " + h.Kind + " is not a unix session")
	}
	identity := procgroup.SessionIdentity{LeaderPID: h.LeaderPID, SessionID: h.SessionID, BirthToken: h.LeaderBirth}
	empty, err := procgroup.SessionEmpty(identity)
	if err != nil || empty {
		return err
	}
	return procgroup.TerminateSession(identity)
}

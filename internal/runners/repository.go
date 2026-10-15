package runners

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// ErrRepoNotAllowed marks source the machine owner has not authorized.
var ErrRepoNotAllowed = errors.New("repository not allowed on this machine")

type machineHost struct {
	host         UnitHost
	repositories sourceurl.RepoAllowlist
}

func (h machineHost) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	unit, err := captureUnit(unit)
	if err != nil {
		return err
	}
	requests := []runspec.SealedRequest{unit.Request}
	if unit.Accepted != nil {
		requests = append(requests, unit.Accepted.Spec.Request)
	}
	for _, request := range requests {
		spec, err := source.ForRequest(request)
		if err == nil && spec.LocalCheckout == "" {
			identity, identityErr := sourceurl.Identity(spec.RemoteURL)
			if identityErr == nil && h.repositories.Admits(identity) {
				continue
			}
		}
		return fmt.Errorf("%w: run %s requires a sealed remote source admitted by runner.repos (%s)", ErrRepoNotAllowed, unit.RunID, h.repositories)
	}
	return h.host.Host(ctx, unit)
}

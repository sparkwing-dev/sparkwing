package store

import (
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const (
	provenRepositoryHostReason   = "personal machines and here hosts cannot execute repository-proven runs; use a shared runner or Sparkwing Cloud"
	repositoryScopedRunnerReason = "repository-scoped runners require repository-proven code"
)

func checkRunHost(c capability.Capability, p runspec.Provenance) error {
	if p.ProvenRepository() != "" && (c.Host == nil || !c.Host.Kind.AllowsProvenRepository()) {
		return authorityError(apierr.Forbidden, "repository-proven runs require a shared runner or Cloud host")
	}
	return nil
}

package runners

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// StoreEngine is the controller boundary used by the embedded claimant and host.
type StoreEngine interface {
	ValidateEmbeddedRunner() error
	ClaimPolicyRouter() (*routepolicy.Router, error)
	RunnerTenant(context.Context, store.Team) (*store.Tenant, error)
	CheckRunner(context.Context, capability.AuthorityRef, string) (capability.Principal, error)
	Claim(context.Context, capability.Authorized, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
}

package capability

import (
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// RequiredAuthority describes grant alternatives without consulting resource state.
// Each alternative requires all its grants, for example []Grant{DataRead, DataWrite}.
func RequiredAuthority(reason apierr.Reason, alternatives ...[]Grant) *apierr.Error {
	var requirements []string
	for _, grants := range alternatives {
		var names []string
		for _, grant := range grants {
			names = append(names, string(grant))
		}
		requirement := strings.Join(names, " and ")
		if requirement != "" && !slices.Contains(requirements, requirement) {
			requirements = append(requirements, requirement)
		}
	}
	if len(requirements) == 0 {
		return forbidden(reason)
	}
	message := "operation requires " + strings.Join(requirements, " or ")
	if len(requirements) == 1 {
		switch Grant(requirements[0]) {
		case TeamAdmin:
			message += "; ask a team owner for a credential with team.admin"
		case OIDCMint:
			message += "; use an awarded or delegated job credential with oidc.mint"
		case DataRead:
			message += "; use a job credential with repository authority or ask a team editor or owner with repository authority to mint a repository cache reader through POST /api/v1/tokens with a remote_cache data scope"
		}
	}
	return apierr.Authorization(reason, message)
}

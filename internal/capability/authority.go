package capability

import (
	"slices"
	"time"
)

// DeploymentAuthority identifies unbound operational deployment credentials.
func DeploymentAuthority(p Principal) bool {
	c := p.Capability
	return p.Class == CredentialBearer && c.Team == "" && c.Audience == Controller && c.Unit == "" && c.Purpose == "" && c.Binding.Kind == BindingNone && (c.Subject.Kind == SubjectOperator || c.Subject.Kind == SubjectService) && c.Subject.ID != ""
}

// ConsoleProof names the direct session whose console authority must be revalidated.
type ConsoleProof struct{ Session AuthorityRef }

// ConsoleAuthority checks direct origin, current configuration and session lifetime.
func ConsoleAuthority(p Principal, configured bool, now time.Time, lifetime time.Duration) bool {
	c := p.Capability
	return configured && p.DirectAccountSession() && c.Audience == Controller &&
		(c.ExpiresAt == nil || now.Before(*c.ExpiresAt)) && (c.RevokedAt == nil || now.Before(*c.RevokedAt)) &&
		(lifetime <= 0 || now.Before(c.IssuedAt.Add(lifetime)))
}

// OwnsToken applies the same ownership relation to routes and committing transactions.
func OwnsToken(p Principal, target Metadata) bool {
	c := p.Capability
	if c.Team != target.Team || c.Binding.Kind != BindingNone {
		return false
	}
	if target.Team == "" {
		return DeploymentAuthority(p) && slices.Contains(c.Grants, OperatorTokens) || p.Class == CredentialConsole && slices.Contains(c.Grants, OperatorConsole)
	}
	return slices.Contains(c.Grants, TeamAdmin) || c.Subject.Kind == SubjectAccount && (target.Subject == c.Subject || target.CreatedBy == c.Subject.ID) || p.Authority == (AuthorityRef{Kind: AuthorityCapability, ID: target.Prefix})
}

// ProofForConsole carries the authenticated console class into a committing operation.
func ProofForConsole(p Principal) *ConsoleProof {
	if p.Class != CredentialConsole {
		return nil
	}
	return &ConsoleProof{Session: p.Authority}
}

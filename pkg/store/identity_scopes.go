package store

import "slices"

// ScopeRunsRead and the other membership scopes keep authentication and token minting in agreement.
const (
	ScopeRunsRead       = "runs.read"
	ScopeLogsRead       = "logs.read"
	ScopeTriggersRead   = "triggers.read"
	ScopeRunsWrite      = "runs.write"
	ScopeRunsControl    = "runs.control"
	ScopeApprovalsWrite = "approvals.write"
	ScopeTeamAdmin      = "team.admin"
)

// safety: no role grants deployment administration; unknown roles grant nothing.
var roleScopes = map[Role][]string{
	RoleReader: {ScopeRunsRead, ScopeLogsRead, ScopeTriggersRead},
	RoleEditor: {ScopeRunsRead, ScopeLogsRead, ScopeTriggersRead, ScopeRunsWrite, ScopeRunsControl, ScopeApprovalsWrite},
	RoleOwner:  {ScopeRunsRead, ScopeLogsRead, ScopeTriggersRead, ScopeRunsWrite, ScopeRunsControl, ScopeApprovalsWrite, ScopeTeamAdmin},
}

// ScopesForRole returns an independent copy of the role's scopes, or none for an unknown role.
func ScopesForRole(role Role) []string { return slices.Clone(roleScopes[role]) }

// CLITokenScopes excludes team administration even when the member is an owner.
// The returned slice is independent of the membership policy.
func CLITokenScopes(role Role) []string {
	return slices.DeleteFunc(ScopesForRole(role), func(s string) bool { return s == ScopeTeamAdmin })
}

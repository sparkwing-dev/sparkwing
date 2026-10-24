package store

import "slices"

// scopeRunsRead and the other membership scopes keep authentication and token minting in agreement.
const (
	scopeRunsRead = "runs.read"
	scopeLogsRead = "logs.read"

	scopeRunsWrite      = "runs.write"
	scopeRunsControl    = "runs.control"
	scopeApprovalsWrite = "approvals.write"
	scopeTeamAdmin      = "team.admin"
)

// safety: no role grants deployment administration; unknown roles grant nothing.
var roleScopes = map[Role][]string{
	RoleReader: {scopeRunsRead, scopeLogsRead},
	RoleEditor: {scopeRunsRead, scopeLogsRead, scopeRunsWrite, scopeRunsControl, scopeApprovalsWrite},
	RoleOwner:  {scopeRunsRead, scopeLogsRead, scopeRunsWrite, scopeRunsControl, scopeApprovalsWrite, scopeTeamAdmin},
}

// ScopesForRole returns an independent copy of the role's scopes, or none for an unknown role.
func ScopesForRole(role Role) []string { return slices.Clone(roleScopes[role]) }

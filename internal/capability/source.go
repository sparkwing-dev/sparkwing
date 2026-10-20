package capability

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// SourceExecutor comes from registration and award records, never advertised runner labels.
type SourceExecutor struct {
	Team                   string
	MachineID              string
	Cloud                  bool
	OwnerAccountID         string
	Shared                 bool
	AuthorizedRepositories []string
}

// SourceReleaseRequest uses a live claim and the sealed repo identity for credential release.
type SourceReleaseRequest struct {
	Authorization    Authorized
	Executor         SourceExecutor
	RepositoryKey    string
	CredentialID     string
	Host             string
	GitHubApp        bool
	SSH              bool
	SSHHostConfirmed bool
}

// SourceReleaseAudit must commit before a team credential leaves the controller.
// Secret material is excluded so the audit survives credential rotation safely.
type SourceReleaseAudit struct {
	ID               string
	Team             string
	CredentialID     string
	Host             string
	RepositoryKey    string
	RunID            string
	NodeID           string
	Generation       int64
	MachineID        string
	OwnerAccountID   string
	CapabilityPrefix string
	ReleasedAt       time.Time
}

// AuthorizeSourceRelease requires source.read, live sensitive authority, repo permission and
// a same-team Cloud executor or shared runner; a personal machine never qualifies.
func AuthorizeSourceRelease(request SourceReleaseRequest, now time.Time) (SourceReleaseAudit, error) {
	a := request.Authorization
	c := a.Principal.Capability
	if !isClaim(a.Principal) || a.Access.Liveness != LiveSensitive || a.Access.Binding != BindSource ||
		a.ResultReplay || a.LogTailAttempt != 0 || !slices.Contains(a.Access.Grants, SourceRead) || !slices.Contains(c.Grants, SourceRead) {
		return SourceReleaseAudit{}, forbidden(apierr.ReasonMissingGrant)
	}
	if c.Audience != Controller {
		return SourceReleaseAudit{}, forbidden(apierr.ReasonAudience)
	}
	if !CredentialAlive(c, now) {
		return SourceReleaseAudit{}, unauthenticated()
	}
	executor := request.Executor
	if c.Team == "" || executor.Team != c.Team || a.Target.Team != c.Team {
		return SourceReleaseAudit{}, forbidden(apierr.ReasonTeam)
	}
	if request.RepositoryKey == "" || !slices.Contains(c.Repositories, request.RepositoryKey) ||
		!slices.Contains(executor.AuthorizedRepositories, request.RepositoryKey) {
		return SourceReleaseAudit{}, forbidden(apierr.ReasonRepositoryUnauthorized)
	}
	if executor.MachineID == "" || !executor.Cloud && !executor.Shared {
		return SourceReleaseAudit{}, forbidden(apierr.ReasonPersonalMachine)
	}
	if request.SSH && !request.SSHHostConfirmed {
		return SourceReleaseAudit{}, forbidden(apierr.ReasonCredentialUnconfirmed)
	}
	if request.CredentialID == "" || request.Host == "" || c.Prefix == "" {
		return SourceReleaseAudit{}, policyFailure()
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return SourceReleaseAudit{}, err
	}
	return SourceReleaseAudit{
		ID: hex.EncodeToString(id[:]), Team: c.Team, CredentialID: request.CredentialID, Host: request.Host,
		RepositoryKey: request.RepositoryKey, RunID: c.Binding.RunID, NodeID: c.Binding.NodeID, Generation: c.Binding.Generation,
		MachineID: executor.MachineID, OwnerAccountID: executor.OwnerAccountID, CapabilityPrefix: c.Prefix, ReleasedAt: now,
	}, nil
}

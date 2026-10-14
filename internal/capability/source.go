package capability

import "time"

// SourceExecutor comes from registration and award records, never advertised runner labels.
type SourceExecutor struct {
	Team                   string
	MachineID              string
	Cloud                  bool
	OwnerAccountID         string
	OwnerOptedIn           bool
	AuthorizedRepositories []string
}

// SourceReleaseRequest uses a live claim and the sealed repo identity for credential release.
type SourceReleaseRequest struct {
	Authorization    Authorized
	Executor         SourceExecutor
	RepositoryKey    string
	CredentialID     string
	Host             string
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
// either a same-team cloud executor or its owner's explicit machine opt-in.
// Team-admin token management alone cannot consent on the machine owner's behalf.
func AuthorizeSourceRelease(request SourceReleaseRequest, now time.Time) (SourceReleaseAudit, error) {
	panic("unimplemented: c2-credentials")
}

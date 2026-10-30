package capability

// HostKind is the execution boundary classified when a unit is claimed.
type HostKind string

const (
	HostCloud      HostKind = "cloud"
	HostSelfHosted HostKind = "self-hosted"
	HostPersonal   HostKind = "personal"
	HostHere       HostKind = "here"
)

// HostFacts is sealed by award issuance and inherited unchanged by delegates.
// CacheScope includes the team's host-sharing policy at claim time.
type HostFacts struct {
	Kind           HostKind `json:"kind"`
	Fresh          bool     `json:"fresh,omitempty"`
	RunnerKind     string   `json:"runner_kind"`
	CacheScope     string   `json:"cache_scope,omitempty"`
	OwnerAccountID string   `json:"owner_account_id,omitempty"`
	MachineID      string   `json:"machine_id,omitempty"`
}

// AllowsProvenRepository reports whether this boundary may execute repository-proven code.
func (h HostKind) AllowsProvenRepository() bool { return h == HostCloud || h == HostSelfHosted }

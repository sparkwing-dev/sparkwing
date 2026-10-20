// Package capability defines authority shared by the controller, store and node host.
package capability

import (
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// SubjectKind keeps accountability independent of token spelling and placement.
type SubjectKind string

const (
	SubjectAccount   SubjectKind = "account"
	SubjectMachine   SubjectKind = "machine"
	SubjectService   SubjectKind = "service"
	SubjectOperator  SubjectKind = "operator"
	SubjectLocal     SubjectKind = "local"
	SubjectAnonymous SubjectKind = "anonymous"
)

// Subject identifies the accountable account, machine, service, Unix uid or Windows SID.
type Subject struct {
	Kind SubjectKind `json:"kind"`
	ID   string      `json:"id"`
}

// Audience prevents a pipeline bearer from opening controller routes.
type Audience string

const (
	Controller       Audience = "controller"
	NodeHost         Audience = "nodehost"
	ControllerPrefix          = "swk_"
	NodeHostPrefix            = "swn_"
)

// BindingKind separates a resource fence from the grants it carries.
type BindingKind string

const (
	BindingNone    BindingKind = "none"
	BindingTrigger BindingKind = "trigger"
	BindingNode    BindingKind = "node"
	BindingPush    BindingKind = "push"
	BindingRun     BindingKind = "run"
)

// LeaseRef identifies the live authority when the target itself is terminal, as for expansion.
type LeaseRef struct {
	Kind       UnitKind `json:"kind"`
	ID         string   `json:"id"`
	Generation int64    `json:"generation"`
	Holder     string   `json:"holder"`
}

// PushScope confines an Actions exchange without manufacturing App branch trust.
type PushScope struct {
	RepositoryKey string `json:"repo_key"`
	Ref           string `json:"ref"`
	SHA           string `json:"sha"`
	WorkflowRunID string `json:"workflow_run_id"`
}

// Binding is server-derived; incoming fence headers cannot replace any field.
// NodeID is internal vocabulary and is serialized as job_id.
type Binding struct {
	Kind        BindingKind `json:"kind"`
	RunID       string      `json:"run_id,omitempty"`
	NodeID      string      `json:"job_id,omitempty"`
	Generation  int64       `json:"generation,omitempty"`
	Holder      string      `json:"holder,omitempty"`
	Attempt     int64       `json:"attempt,omitempty"`
	ClaimBy     *time.Time  `json:"claim_by,omitempty"`
	Deadline    *time.Time  `json:"deadline,omitempty"`
	Reservation string      `json:"reservation,omitempty"`
	Push        *PushScope  `json:"push,omitempty"`
	Expansion   *LeaseRef   `json:"expansion,omitempty"`
}

// AuthorityKind includes sessions because here awards must end with the requesting session.
type AuthorityKind string

const (
	AuthorityCapability AuthorityKind = "capability"
	AuthoritySession    AuthorityKind = "session"
)

// AuthorityRef names revocation ancestry without copying a session into the capability table.
type AuthorityRef struct {
	Kind AuthorityKind `json:"kind"`
	ID   string        `json:"id"`
}

// Capability contains no reusable secret. Digest is SHA-256 of a generated reference token.
type Capability struct {
	Digest        [32]byte                  `json:"-"`
	Prefix        string                    `json:"prefix"`
	Team          string                    `json:"team"`
	Subject       Subject                   `json:"subject"`
	Audience      Audience                  `json:"audience"`
	Grants        []Grant                   `json:"grants"`
	Binding       Binding                   `json:"binding"`
	Issuance      IssuanceKind              `json:"issuance"`
	Unit          UnitKind                  `json:"unit,omitempty"`
	UnitID        string                    `json:"unit_id,omitempty"`
	Purpose       AwardPurpose              `json:"purpose,omitempty"`
	Delivery      DeliveryPurpose           `json:"delivery,omitempty"`
	Parent        *AuthorityRef             `json:"parent,omitempty"`
	ReservedPools []string                  `json:"reserved_pools"`
	Trust         Trust                     `json:"trust,omitempty"`
	Repositories  []string                  `json:"repositories"`
	Children      []nodeproto.ChildPipeline `json:"children,omitempty"`
	Data          *DataScope                `json:"data,omitempty"`
	Billing       *BillingProvenance        `json:"billing,omitempty"`
	RootMetered   bool                      `json:"root_metered"`
	IssuedAt      time.Time                 `json:"issued_at"`
	ExpiresAt     *time.Time                `json:"expires_at,omitempty"`
	RevokedAt     *time.Time                `json:"revoked_at,omitempty"`
	// ExecutionHost is sealed by the award owner, independently of the accountable subject.
	ExecutionHost string `json:"execution_host,omitempty"`
}

// CredentialClass prevents deployment tokens from substituting for console sessions.
type CredentialClass string

const (
	CredentialBearer    CredentialClass = "bearer"
	CredentialSession   CredentialClass = "session"
	CredentialConsole   CredentialClass = "operator_console"
	CredentialPeer      CredentialClass = "local_peer"
	CredentialAnonymous CredentialClass = "anonymous"
)

// Principal is the common policy input for reference tokens, sessions and local peers.
// Peer identity requires Unix peer credentials or a verified Windows SID.
type Principal struct {
	Capability Capability
	Class      CredentialClass
	Authority  AuthorityRef
}

// DirectAccountSession identifies account authentication without capability ancestry.
func (p Principal) DirectAccountSession() bool {
	c := p.Capability
	return p.Class == CredentialSession && p.Authority.Kind == AuthoritySession && p.Authority.ID != "" &&
		c.Subject.Kind == SubjectAccount && c.Subject.ID != "" && c.Parent == nil &&
		c.Unit == "" && c.Purpose == "" && c.Binding.Kind == BindingNone
}

// Issued returns the generated secret once; callers must keep Raw out of logs and process env.
type Issued struct {
	Raw        string     `json:"capability"`
	Capability Capability `json:"-"`
}

// Trust is the runs a machine root accepts work from, set by a team admin at registration.
// The empty value takes the controller default: verified default-branch work only when the
// root holds a pool that a secret is reserved to, and any run otherwise.
type Trust string

const (
	TrustVerifiedDefault Trust = "verified-default"
	TrustAny             Trust = "any"
)

// Valid reports whether t is the controller default or a named class.
func (t Trust) Valid() bool { return t == "" || t == TrustVerifiedDefault || t == TrustAny }

// Launcher identifies roots that can deliver only per-claim bootstrap authority.
func Launcher(c Capability) bool {
	if c.Unit != "" || c.Binding.Kind != BindingNone || !slices.Contains(c.Grants, ClaimsLaunch) {
		return false
	}
	return c.Subject.Kind == SubjectService || c.Subject.Kind == SubjectMachine && c.Team != "" && len(c.ReservedPools) > 0
}

// CloudLauncher distinguishes deployment capacity from a team's own pool.
func CloudLauncher(c Capability) bool {
	return Launcher(c) && c.Subject.Kind == SubjectService
}

// AllowsChild checks execution authority independently of source and data scope.
func (c Capability) AllowsChild(pipeline, repository string) bool {
	return slices.Contains(c.Children, nodeproto.ChildPipeline{Pipeline: pipeline, Repository: repository})
}

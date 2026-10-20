package localhostwire

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// LocalDataAuthority carries the controller's canonical live storage projection.
type LocalDataAuthority struct {
	Authority blob.Authority `json:"authority"`
}

// LocalNodeSnapshot keeps artifact selectors outside the executable job namespace.
type LocalNodeSnapshot struct {
	store.RunSnapshot
	ArtifactSources map[string]blob.RecordKey `json:"artifact_sources,omitempty"`
}

// LocalRunTransition carries a lifecycle edge without actor authority.
type LocalRunTransition struct {
	To          store.RunState           `json:"to"`
	Cause       store.RunCause           `json:"cause"`
	Reason      string                   `json:"reason"`
	MaskedError string                   `json:"masked_error"`
	OwnerPolicy *store.OwnerPolicyResult `json:"owner_policy,omitempty"`
}

// LocalNodeTransition carries an edge and its captured source attempt.
type LocalNodeTransition struct {
	To            store.NodeState       `json:"to"`
	Cause         store.TransitionCause `json:"cause"`
	SourceAttempt *int64                `json:"source_attempt,omitempty"`
	Extra         LocalTransitionExtra  `json:"extra"`
}

// LocalTransitionExtra omits award and execution authority.
type LocalTransitionExtra struct {
	OwnerPolicy *store.OwnerPolicyResult  `json:"owner_policy,omitempty"`
	Result      *store.NodeResult         `json:"result,omitempty"`
	Materialize *store.MaterializeSource  `json:"materialize,omitempty"`
	Approval    *store.ApprovalResolution `json:"approval,omitempty"`
	RetryAt     *time.Time                `json:"retry_at,omitempty"`
}

// LocalAuthenticationRequest selects an authority within the host's awarded run.
type LocalAuthenticationRequest struct {
	Lookup   capability.CredentialLookup `json:"lookup"`
	Evidence capability.RequestEvidence  `json:"evidence"`
}

// LocalAuthentication preserves digest fields omitted by public capability JSON.
type LocalAuthentication struct {
	Snapshot capability.AuthenticationSnapshot `json:"snapshot"`
	Digests  [][32]byte                        `json:"digests"`
}

// LocalBindingRequest compares a selected authority with fresh persisted relations.
type LocalBindingRequest struct {
	Authority capability.AuthorityRef `json:"authority"`
	Rule      capability.BindingRule  `json:"rule"`
	Target    capability.Target       `json:"target"`
}

// LocalBindingChecked acknowledges a successful persisted binding check.
type LocalBindingChecked struct {
	Checked bool `json:"checked"`
}

// LocalCapacityRequest selects work under the authenticated owner's fence.
type LocalCapacityRequest struct {
	Key     string                `json:"key"`
	Binding store.CapacityBinding `json:"binding"`
}

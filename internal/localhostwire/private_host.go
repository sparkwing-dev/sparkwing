package localhostwire

import (
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// LocalDataAuthority carries the controller's canonical live storage projection.
type LocalDataAuthority struct {
	Authority blob.Authority `json:"authority"`
}

// LocalNodeSnapshot keeps artifact selectors outside the executable job namespace.
type LocalNodeSnapshot struct {
	runstate.RunSnapshot
	ArtifactSources map[string]blob.RecordKey `json:"artifact_sources,omitempty"`
}

// LocalRunTransition carries a lifecycle edge without actor authority.
type LocalRunTransition struct {
	To          runstate.RunState `json:"to"`
	Cause       runstate.RunCause `json:"cause"`
	Reason      string            `json:"reason"`
	MaskedError string            `json:"masked_error"`
}

// LocalRunCompletion requests the store-derived verdict without choosing an outcome.
type LocalRunCompletion struct {
	Cause       runstate.RunCause `json:"cause"`
	Reason      string            `json:"reason"`
	MaskedError string            `json:"masked_error"`
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

// LocalCapacityRequest names a job under its authenticated award.
type LocalCapacityRequest struct {
	Binding runstate.CapacityBinding `json:"binding"`
}

// LocalEvaluationRelease proposes expansion work under the current run owner.
type LocalEvaluationRelease struct {
	NodeID string `json:"job_id"`
}

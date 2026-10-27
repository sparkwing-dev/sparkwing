package controllerwire

import "context"

// SecretMetadata is safe to list. OnlyOn is a pool restriction, enforced by
// plan acceptance and release against the awarded claim, never machine labels.
// A pipeline secret names its one repository: RepoKey is the canonical key a
// run's repository must match, and Repo its display when the row was written.
type SecretMetadata struct {
	Name      string `json:"name"`
	Principal string `json:"principal"`
	Repo      string `json:"repo,omitempty"`
	RepoKey   string `json:"repo_key,omitempty"`
	Pipeline  string `json:"pipeline,omitempty"`
	Masked    bool   `json:"masked"`
	Shared    bool   `json:"shared"`
	Bound     bool   `json:"bound"`
	OnlyOn    string `json:"only_on,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type Secrets struct {
	Secrets []SecretMetadata `json:"secrets"`
}

// SecretSelector distinguishes pipeline-owned and unscoped rows of one name.
// Repo and Pipeline are both set or both empty. Repo is GitHub owner/name or a
// gh:<id> key; the controller resolves it to the key.
type SecretSelector struct {
	Name     string `json:"name"`
	Repo     string `json:"repo,omitempty"`
	Pipeline string `json:"pipeline,omitempty"`
}

// SecretValue is returned only by an authorized explicit value read. An empty
// value is valid and must stay present in JSON.
type SecretValue struct {
	SecretMetadata
	Value string `json:"value"`
}

// SecretWrite carries requested visibility and placement, without supplying an
// owner, ciphertext or authority. The controller derives and encrypts those.
type SecretWrite struct {
	Name           string `json:"name"`
	Value          string `json:"value"`
	Repo           string `json:"repo,omitempty"`
	Pipeline       string `json:"pipeline,omitempty"`
	Masked         bool   `json:"masked"`
	Shared         bool   `json:"shared"`
	OnlyOn         string `json:"only_on,omitempty"`
	IdempotencyKey string `json:"-"`
}

type SecretWritten struct {
	Warning  string         `json:"warning,omitempty"`
	Secret   SecretMetadata `json:"secret"`
	Replayed bool           `json:"replayed"`
}

type SecretDeletion struct {
	Name     string `json:"name"`
	Repo     string `json:"repo,omitempty"`
	Pipeline string `json:"pipeline,omitempty"`
	Deleted  bool   `json:"deleted"`
}

type SecretRotationRequest struct {
	IdempotencyKey string `json:"-"`
}

// SecretRotation keeps skipped row identities so a partial rotation cannot be
// mistaken for evidence that the previous encryption key can be dropped.
type SecretRotation struct {
	Rotated  int              `json:"rotated"`
	Skipped  []SecretSelector `json:"skipped"`
	Replayed bool             `json:"replayed"`
}

// SecretManager is shared by api.sock and remote management clients. The
// controller owns encryption, authorization, preflight and restricted release.
type SecretManager interface {
	ListSecrets(context.Context) (Secrets, error)
	ReadSecret(context.Context, SecretSelector) (SecretValue, error)
	WriteSecret(context.Context, SecretWrite) (SecretWritten, error)
	DeleteSecret(context.Context, SecretSelector) (SecretDeletion, error)
	RotateSecrets(context.Context, SecretRotationRequest) (SecretRotation, error)
}

package nodehost

import (
	"errors"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// MaskRegistry serializes run-local registrations and topology acceptance.
type MaskRegistry struct {
	mu          sync.Mutex
	masker      *secrets.Masker
	identifiers map[string]string
}

// NewMaskRegistry uses the same masker that host logs and returned errors use.
func NewMaskRegistry(masker *secrets.Masker) *MaskRegistry {
	return &MaskRegistry{masker: masker, identifiers: map[string]string{}}
}

// ContainsSecret implements the shared plan validator's matcher, including short values.
func (r *MaskRegistry) ContainsSecret(identifier string) bool {
	return r.masker.ContainsSecret(identifier)
}

// Register refuses collisions before changing any sink; success is synchronous.
func (r *MaskRegistry) Register(value string) error {
	candidate := secrets.NewMasker()
	if err := candidate.Register(value); err != nil {
		return err
	}
	return r.register(candidate)
}

// registerIssued protects a service value without changing accepted topology.
func (r *MaskRegistry) registerIssued(value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.masker == nil {
		return errors.New("mask registration requires a shared masker")
	}
	return r.masker.Register(value)
}

func (r *MaskRegistry) register(candidate *secrets.Masker) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.masker == nil {
		return errors.New("mask registration requires a shared masker")
	}
	for identifier, path := range r.identifiers {
		if candidate.ContainsSecret(identifier) {
			return identifierMaskError(path)
		}
	}
	r.masker.Merge(candidate)
	return nil
}

// Accept checks initial or generated identifiers, commits under the registration
// lock, then retains their names. A refused commit changes no registry state.
func (r *MaskRegistry) Accept(plan nodeproto.PlanDoc, commit func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.masker == nil || commit == nil {
		return errors.New("identifier acceptance requires masker and commit")
	}
	identifiers := map[string]string{}
	if err := nodeproto.VisitPlanIdentifiers(plan, func(identifier, path string) error {
		if r.ContainsSecret(identifier) {
			return identifierMaskError(path)
		}
		identifiers[identifier] = path
		return nil
	}); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return err
	}
	for identifier, path := range identifiers {
		r.identifiers[identifier] = path
	}
	return nil
}

func identifierMaskError(path string) error {
	return executorError(apierr.SecretInIdentifier, "registered secret conflicts with identifier at "+path)
}

func (r *MaskRegistry) mask(text string) string { return r.masker.Mask(text) }

// safety: field paths are host-generated; values cannot enter collision diagnostics.
var _ nodeproto.SecretMatcher = (*MaskRegistry)(nil)

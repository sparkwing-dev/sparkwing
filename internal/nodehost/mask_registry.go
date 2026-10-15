package nodehost

import (
	"errors"
	"fmt"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// MaskRegistry shares one masker across process output, records and plan validation.
// Accept serializes topology commits with dynamic registrations so neither can
// acknowledge an identifier that the other operation made secret.
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
	return (planningSecrets{masker: r.masker}).ContainsSecret(identifier)
}

// Register refuses collisions before changing any sink; success is synchronous.
func (r *MaskRegistry) Register(value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.masker == nil {
		return errors.New("mask registration requires a shared masker")
	}
	candidate := NewMaskRegistry(secrets.NewMasker())
	candidate.masker.Register(value)
	for identifier, path := range r.identifiers {
		if candidate.ContainsSecret(identifier) {
			return identifierMaskError(path)
		}
	}
	r.masker.Register(value)
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
	identifiers := planIdentifiers(plan)
	for identifier, path := range identifiers {
		if r.ContainsSecret(identifier) {
			return identifierMaskError(path)
		}
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

func planIdentifiers(plan nodeproto.PlanDoc) map[string]string {
	identifiers := map[string]string{}
	add := func(value, path string) {
		if value != "" {
			identifiers[value] = path
		}
	}
	groups := func(values []string, path string) {
		for i, value := range values {
			add(value, fmt.Sprintf("%s[%d]", path, i))
		}
	}
	for i, job := range plan.Jobs {
		path := fmt.Sprintf("jobs[%d]", i)
		add(job.ID, path+".id")
		groups(job.Groups, path+".groups")
		groups(job.Needs, path+".deps")
		groups(job.NeedsIfPresent, path+".deps_if_present")
		groups(job.NeedsGenerated, path+".needs_generated")
		groups(job.Closures.Generate, path+".closures.generate")
		add(job.OnFailureOf, path+".on_failure_of")
		for j, step := range job.Steps {
			stepPath := fmt.Sprintf("%s.steps[%d]", path, j)
			add(step.ID, stepPath+".id")
			groups(step.Groups, stepPath+".groups")
			groups(step.Needs, stepPath+".deps")
		}
		for j, group := range job.Concurrency {
			add(group.Group, fmt.Sprintf("%s.concurrency[%d].group", path, j))
		}
	}
	for i, group := range plan.Concurrency {
		add(group.Group, fmt.Sprintf("concurrency[%d].group", i))
	}
	return identifiers
}

func (r *MaskRegistry) mask(text string) string { return r.masker.Mask(text) }

// safety: field paths are host-generated; values cannot enter collision diagnostics.
var _ nodeproto.SecretMatcher = (*MaskRegistry)(nil)

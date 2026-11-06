package localcontroller

import (
	"context"
	"errors"
	"runtime"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// ClaimProfile binds configured placement labels to the controller's OS account.
type ClaimProfile struct {
	subject string
	labels  []string
}

// NewClaimProfile captures the OS account and filters configured placement labels.
func NewClaimProfile(labels []string) (*ClaimProfile, error) {
	subject, err := wingd.CurrentLocalSubject()
	if err != nil {
		return nil, err
	}
	return &ClaimProfile{subject: subject, labels: match.SelfAsserted(labels)}, nil
}

// ResolveClaimProfile binds local placement facts to the captured OS account.
// Configured labels join the observed platform; the store grants name, class,
// location and pools from the capability, so labels never widen authority.
func (c *ClaimProfile) ResolveClaimProfile(ctx context.Context, principal capability.Principal) (match.Profile, error) {
	if err := context.Cause(ctx); err != nil {
		return match.Profile{}, err
	}
	p := principal.Capability
	if c.subject == "" || p.Subject != (capability.Subject{Kind: capability.SubjectLocal, ID: c.subject}) || p.Team != string(store.DefaultTeam) || p.Audience != capability.Controller || principal.Authority != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: p.Prefix}) || p.Prefix == "" {
		return match.Profile{}, errors.New("local claim profile requires the controller account's authenticated capability")
	}
	return match.Profile{Name: c.subject, Labels: slices.Clone(c.labels), OS: runtime.GOOS, Arch: runtime.GOARCH}, nil
}

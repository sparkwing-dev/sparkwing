package localcontroller

import (
	"context"
	"errors"
	"runtime"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Claimant owns the local queue's delegated credential for one controller lifetime.
type Claimant struct {
	store    *store.Store
	subject  string
	mu       sync.Mutex
	issued   capability.Issued
	closed   bool
	closeErr error
}

// NewClaimant captures the process's verified OS identity without request input.
func NewClaimant(s *store.Store) (*Claimant, error) {
	if s == nil {
		return nil, errors.New("local claimant requires its controller store")
	}
	subject, err := wingd.CurrentLocalSubject()
	if err != nil {
		return nil, err
	}
	return &Claimant{store: s, subject: subject}, nil
}

// Bearer delegates queue authority through the shared peer authentication and issuance policy.
func (c *Claimant) Bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.store == nil || c.subject == "" {
		return "", errors.New("local claimant requires an open store and OS subject")
	}
	if c.issued.Raw != "" {
		return c.issued.Raw, context.Cause(ctx)
	}
	route, ok := capability.LookupRoute("POST", "/api/v1/tokens")
	if !ok {
		return "", errors.New("local credential issuance policy is missing")
	}
	target := capability.Target{Team: string(store.DefaultTeam)}
	auth, err := capability.NewAuthenticator(c.store).Authenticate(ctx, capability.Presentation{
		Class: capability.CredentialPeer, Peer: capability.Subject{Kind: capability.SubjectLocal, ID: c.subject},
		Evidence: capability.RequestEvidence{Target: target},
	}, route)
	if err != nil {
		return "", err
	}
	authorized, err := capability.Authorize(ctx, c.store, route, auth, target)
	if err != nil {
		return "", err
	}
	p := authorized.Principal
	c.issued, err = c.store.DelegateCapability(ctx, capability.DelegateSpec{
		Parent: p.Authority, Audience: capability.Controller,
		Grants:  []capability.Grant{capability.TriggersClaim, capability.NodesClaim},
		Binding: p.Capability.Binding, ExpiresAt: p.Capability.ExpiresAt,
	})
	return c.issued.Raw, err
}

// Close revokes the queue credential after all hosts have joined.
func (c *Claimant) Close() (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.closeErr
	}
	c.closed = true
	defer func() { c.closeErr = err }()
	if c.issued.Raw == "" {
		return nil
	}
	tenant, err := c.store.ForTeam(context.Background(), store.DefaultTeam)
	if err != nil {
		return err
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.issued.Capability.Prefix}
	return tenant.RevokeAuthority(context.Background(), store.Actor{Kind: store.ActorOperator, Authority: ref}, ref)
}

// ResolveClaimProfile binds local queue capacity to the captured OS account.
func (c *Claimant) ResolveClaimProfile(ctx context.Context, principal capability.Principal) (match.Profile, match.Resources, error) {
	if err := context.Cause(ctx); err != nil {
		return match.Profile{}, match.Resources{}, err
	}
	p := principal.Capability
	if c.subject == "" || p.Subject != (capability.Subject{Kind: capability.SubjectLocal, ID: c.subject}) || p.Team != string(store.DefaultTeam) || p.Audience != capability.Controller || principal.Authority != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: p.Prefix}) || p.Prefix == "" {
		return match.Profile{}, match.Resources{}, errors.New("local claim profile requires the controller account's authenticated capability")
	}
	capacity := match.Resources{Cores: float64(runtime.NumCPU())}
	return match.Profile{Name: c.subject, OS: runtime.GOOS, Arch: runtime.GOARCH, Capacity: capacity}, capacity, nil
}

package nodehost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// ProcessAuthorityDependencies connects process facts to shared credential validation.
type ProcessAuthorityDependencies struct {
	Parent             capability.AuthenticationReader
	Bindings           capability.BindingChecker
	ValidateAuthority  func([]capability.AuthorityState, time.Time) (capability.Principal, error)
	ValidateDelegation func(capability.Principal, capability.DelegateSpec, time.Time) error
}

// ProcessAuthorities retains digest-only delegations until their SDK processes exit.
type ProcessAuthorities struct {
	deps   ProcessAuthorityDependencies
	mu     sync.RWMutex
	live   map[[sha256.Size]byte]*processAuthority
	closed bool
}

type processAuthority struct {
	principal capability.Principal
	parent    capability.CredentialLookup
	services  *ProcessServices
	cancel    context.CancelFunc
}

// NewProcessAuthorities uses the common validators unless an explicit adapter replaces them.
func NewProcessAuthorities(deps ProcessAuthorityDependencies) (*ProcessAuthorities, error) {
	if deps.Parent == nil || deps.Bindings == nil {
		return nil, errors.New("process authority requires fresh parent and relation adapters")
	}
	if deps.ValidateAuthority == nil {
		deps.ValidateAuthority = capability.ValidateAuthority
	}
	if deps.ValidateDelegation == nil {
		deps.ValidateDelegation = capability.ValidateDelegation
	}
	return &ProcessAuthorities{deps: deps, live: map[[sha256.Size]byte]*processAuthority{}}, nil
}

// Delegate narrows controller authority and returns its bearer once with an idempotent revoker.
func (a *ProcessAuthorities) Delegate(ctx context.Context, parent capability.CredentialLookup, spec capability.DelegateSpec, services *ProcessServices) (capability.Issued, func() error, error) {
	if err := context.Cause(ctx); err != nil {
		return capability.Issued{}, nil, err
	}
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return capability.Issued{}, nil, processUnauthenticated()
	}
	if parent.Audience != capability.Controller || spec.Audience != capability.NodeHost || services == nil || services.RunID == "" || services.Job.ID == "" || services.RunID != spec.Binding.RunID || services.Job.ID != spec.Binding.NodeID || services.Masks == nil || services.Masks.masker == nil {
		return capability.Issued{}, nil, executorError(apierr.Forbidden, "process delegation requires its bound job and masker")
	}
	snapshot, err := a.deps.Parent.ReadAuthentication(ctx, parent, capability.RequestEvidence{})
	if err != nil {
		return capability.Issued{}, nil, err
	}
	if len(snapshot.Authorities) == 0 || snapshot.Authorities[0].Principal.Authority != spec.Parent {
		return capability.Issued{}, nil, processUnauthenticated()
	}
	principal, err := a.deps.ValidateAuthority(snapshot.Authorities, snapshot.Now)
	if err == nil {
		err = a.deps.ValidateDelegation(principal, spec, snapshot.Now)
	}
	if err != nil {
		return capability.Issued{}, nil, err
	}
	return a.issue(ctx, parent, spec, principal, snapshot.Now, services)
}

func (a *ProcessAuthorities) issue(ctx context.Context, parent capability.CredentialLookup, spec capability.DelegateSpec, principal capability.Principal, now time.Time, services *ProcessServices) (capability.Issued, func() error, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return capability.Issued{}, nil, err
	}
	raw := capability.NodeHostPrefix + base64.RawURLEncoding.EncodeToString(nonce[:])
	digest := sha256.Sum256([]byte(raw))
	child := principal.Capability
	child.Digest, child.Prefix = digest, capability.NodeHostPrefix+hex.EncodeToString(digest[:8])
	child.Audience, child.Issuance = capability.NodeHost, capability.Delegate
	child.Parent, child.Binding = &spec.Parent, spec.Binding
	child.Grants, child.Repositories, child.Data = spec.Grants, spec.Repositories, spec.Data
	child.IssuedAt, child.ExpiresAt, child.RevokedAt = now, spec.ExpiresAt, nil
	child, err := copyProcessCapability(child)
	if err != nil {
		return capability.Issued{}, nil, err
	}
	retained, err := copyProcessCapability(child)
	if err != nil {
		return capability.Issued{}, nil, err
	}
	bound := *services
	bound.Job = nodeproto.JobSpec{}
	job, err := json.Marshal(services.Job)
	if err != nil {
		return capability.Issued{}, nil, err
	}
	if err := json.Unmarshal(job, &bound.Job); err != nil {
		return capability.Issued{}, nil, err
	}
	bound.Secrets = append([]string{}, services.Secrets...)
	lifetime, cancel := context.WithCancel(context.Background())
	bound.lifetime = lifetime
	entry := &processAuthority{principal: capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}, Capability: retained}, parent: parent, services: &bound, cancel: cancel}
	if err := bound.Masks.Register(raw); err != nil {
		cancel()
		return capability.Issued{}, nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := context.Cause(ctx); err != nil {
		cancel()
		return capability.Issued{}, nil, err
	}
	if a.closed {
		cancel()
		return capability.Issued{}, nil, processUnauthenticated()
	}
	a.live[digest] = entry
	revoke := func() error {
		a.mu.Lock()
		delete(a.live, digest)
		entry.cancel()
		a.mu.Unlock()
		return nil
	}
	return capability.Issued{Raw: raw, Capability: child}, revoke, nil
}

// Close revokes every process and prevents new admissions.
func (a *ProcessAuthorities) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	for digest, entry := range a.live {
		delete(a.live, digest)
		entry.cancel()
	}
	return nil
}

// ReadAuthentication joins fresh parent ancestry with this process's in-memory lifetime.
func (a *ProcessAuthorities) ReadAuthentication(ctx context.Context, lookup capability.CredentialLookup, evidence capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	if err := context.Cause(ctx); err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	a.mu.RLock()
	entry := a.live[lookup.Digest]
	a.mu.RUnlock()
	if lookup.Class != capability.CredentialBearer {
		return capability.AuthenticationSnapshot{}, processUnauthenticated()
	}
	if entry == nil {
		if lookup.Audience == capability.Controller {
			return a.deps.Parent.ReadAuthentication(ctx, lookup, evidence)
		}
		return capability.AuthenticationSnapshot{}, processUnauthenticated()
	}
	snapshot, err := a.deps.Parent.ReadAuthentication(ctx, entry.parent, evidence)
	if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	if len(snapshot.Authorities) == 0 || snapshot.Authorities[0].Principal.Authority != *entry.principal.Capability.Parent {
		return capability.AuthenticationSnapshot{}, processUnauthenticated()
	}
	principal := entry.principal
	principal.Capability, err = copyProcessCapability(principal.Capability)
	if err != nil {
		return capability.AuthenticationSnapshot{}, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.live[lookup.Digest] != entry {
		return capability.AuthenticationSnapshot{}, processUnauthenticated()
	}
	leaf := capability.AuthorityState{Principal: principal, MembershipValid: snapshot.Authorities[0].MembershipValid}
	snapshot.Authorities = append([]capability.AuthorityState{leaf}, snapshot.Authorities...)
	snapshot.Binding.ProcessAlive = true
	return snapshot, context.Cause(ctx)
}

// Resolve selects services only for the exact process principal and authorized attempt fence.
func (a *ProcessAuthorities) Resolve(ctx context.Context, authorized capability.Authorized) (*ProcessServices, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	entry := a.live[authorized.Principal.Capability.Digest]
	if entry == nil || authorized.Principal.Authority != entry.principal.Authority || authorized.Principal.Class != capability.CredentialBearer || authorized.Principal.Capability.Audience != capability.NodeHost {
		return nil, processUnauthenticated()
	}
	if !reflect.DeepEqual(authorized.Binding, entry.principal.Capability.Binding) || !reflect.DeepEqual(authorized.Principal, entry.principal) {
		return nil, executorError(apierr.Forbidden, "process service fence differs from its delegation")
	}
	return entry.services, nil
}

// CheckBinding checks process ownership before consulting the bound resource relations.
func (a *ProcessAuthorities) CheckBinding(ctx context.Context, principal capability.Principal, rule capability.BindingRule, target capability.Target) error {
	service, err := a.Resolve(ctx, capability.Authorized{Principal: principal, Binding: principal.Capability.Binding})
	if err != nil {
		return err
	}
	binding := principal.Capability.Binding
	if target.RunID != "" && target.RunID != binding.RunID || target.Team != "" && target.Team != principal.Capability.Team {
		return processBindingDenied()
	}
	switch rule {
	case capability.BindOwnNode:
		if target.NodeID != "" && target.NodeID != binding.NodeID {
			return processBindingDenied()
		}
	case capability.BindOwnCleanup:
		if service.Cleanups == nil {
			return processBindingDenied()
		}
		if target.CleanupID != "" && !service.Cleanups.contains(target.CleanupID) {
			return processBindingDenied()
		}
	case capability.BindDeclaredSecret:
		if !executorContains(service.Secrets, target.SecretName) {
			return &apierr.Error{Code: apierr.Forbidden, Message: "secret is not declared by the bound job", Details: map[string]any{"reason": apierr.ReasonSecretUndeclared}}
		}
	case capability.BindBodyNamesRun:
		if target.BodyRunID != "" && target.BodyRunID != binding.RunID {
			return processBindingDenied()
		}
		target.BodyRunID = binding.RunID
	case capability.BindReadSet, capability.BindChildOfBoundNode:
	default:
		return processBindingDenied()
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.NodeHost, Digest: principal.Capability.Digest}
	snapshot, err := a.ReadAuthentication(ctx, lookup, capability.RequestEvidence{Target: target})
	if err != nil {
		return err
	}
	if _, err := a.deps.ValidateAuthority(snapshot.Authorities, snapshot.Now); err != nil {
		return err
	}
	if rule == capability.BindOwnCleanup {
		return nil
	}
	target.RunID, target.Team = binding.RunID, principal.Capability.Team
	if rule == capability.BindOwnNode && target.NodeID == "" {
		target.NodeID = binding.NodeID
	}
	return a.deps.Bindings.CheckBinding(ctx, principal, rule, target)
}

func processBindingDenied() error {
	return &apierr.Error{Code: apierr.Forbidden, Message: "resource is outside the process binding", Details: map[string]any{"reason": apierr.ReasonBinding}}
}

func copyProcessCapability(original capability.Capability) (capability.Capability, error) {
	raw, err := json.Marshal(original)
	if err != nil {
		return capability.Capability{}, err
	}
	var copied capability.Capability
	err = json.Unmarshal(raw, &copied)
	copied.Digest = original.Digest
	return copied, err
}

func processUnauthenticated() error {
	return executorError(apierr.Unauthenticated, "process authority is not live")
}

var _ capability.AuthenticationReader = (*ProcessAuthorities)(nil)

var _ capability.BindingChecker = (*ProcessAuthorities)(nil)

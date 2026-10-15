package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StoreCredentialBoundaryScenarios lists SQLite credential proofs below transport and process execution.
func StoreCredentialBoundaryScenarios() []string {
	return []string{"credentials-operator-console-split", "credentials-session-digest-csrf"}
}

type storeProvisionPolicy struct{}

func (storeProvisionPolicy) AuthorizeProvision(ctx context.Context, resolver capability.AuthorityResolver, spec capability.ProvisionSpec) error {
	if spec.RegistrationID != "acceptance" {
		return errors.New("unverified root origin")
	}
	if spec.Authority != nil {
		_, err := resolver.ResolveAuthority(ctx, *spec.Authority)
		return err
	}
	return nil
}

// CheckStoreCredentialBoundary exercises root provisioning and deployment revocation against SQLite.
func CheckStoreCredentialBoundary(ctx context.Context, root, id string) (err error) {
	if id == "credentials-session-digest-csrf" {
		return checkStoreSessions(ctx, root)
	}
	if id != "credentials-operator-console-split" {
		return fmt.Errorf("unknown store credential boundary %q", id)
	}
	s, err := store.Open(filepath.Join(root, "credentials.db"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	s.SetProvisionPolicy(storeProvisionPolicy{})
	bootstrap := capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, RegistrationID: "acceptance", Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens}}
	operator, err := s.ProvisionCapability(ctx, bootstrap)
	if err != nil {
		return err
	}
	operatorRef := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Capability.Prefix}
	s.SetProvisionPolicy(nil)
	serviceSpec := capability.ProvisionSpec{Kind: capability.ProvisionService, RegistrationID: "service:launcher", Authority: &operatorRef, Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch, capability.OperatorTokens}, Metered: true}
	service, err := s.ProvisionCapability(ctx, serviceSpec)
	if err != nil {
		return err
	}
	if service.Capability.Parent != nil || !service.Capability.RootMetered || slices.Contains(service.Capability.Grants, capability.OperatorConsole) {
		return errors.New("service root changed origin or acquired console authority")
	}
	for _, spec := range []capability.ProvisionSpec{bootstrap, serviceSpec} {
		spec.Grants = append(slices.Clone(spec.Grants), capability.OperatorConsole)
		if issued, err := s.ProvisionCapability(ctx, spec); err == nil || issued.Raw != "" {
			return errors.New("deployment provisioning issued console authority")
		}
	}
	var roots int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM capabilities").Scan(&roots); err != nil {
		return err
	}
	if roots != 2 {
		return fmt.Errorf("refused provisioning left %d roots, want 2", roots)
	}
	operatorActor := store.Actor{Authority: operatorRef, CapabilityPrefix: operatorRef.ID}
	if err := s.AsOperator().RevokeAuthority(ctx, operatorActor, operatorRef); err != nil {
		return err
	}
	if _, err := s.LookupCapability(ctx, operator.Capability.Digest); err == nil {
		return errors.New("revoked operator remains live")
	}
	if _, err := s.LookupCapability(ctx, service.Capability.Digest); err != nil {
		return fmt.Errorf("service root inherited its authorizer's revocation: %w", err)
	}
	serviceRef := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: service.Capability.Prefix}
	serviceActor := store.Actor{Authority: serviceRef, CapabilityPrefix: serviceRef.ID}
	if err := s.AsOperator().RevokeAuthority(ctx, serviceActor, serviceRef); err != nil {
		return err
	}
	if _, err := s.LookupCapability(ctx, service.Capability.Digest); err == nil {
		return errors.New("revoked service remains live")
	}
	return nil
}

package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func checkStoreProvisioningOrigins(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	bootstrap := capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}}
	bootstrap.Seed = capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	operator, err := acceptanceBootstrap(ctx, s)
	if err != nil {
		return err
	}
	if ready, err := s.AsOperator().HasDeploymentAdministrator(ctx); err != nil || !ready || operator.Raw != bootstrap.Seed {
		return fmt.Errorf("seeded administrator readiness: %t, %v", ready, err)
	}
	operatorRef := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Capability.Prefix}
	serviceSpec := capability.ProvisionSpec{Kind: capability.ProvisionService, RegistrationID: "service:launcher", Authority: &operatorRef, Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch, capability.OperatorTokens}, Metered: true}
	service, err := s.AsOperator().ProvisionCapability(ctx, serviceSpec)
	if err != nil {
		return fmt.Errorf("service issuance: %w", err)
	}
	if service.Capability.Parent != nil || !service.Capability.RootMetered || slices.Contains(service.Capability.Grants, capability.OperatorConsole) {
		return errors.New("service root changed origin or acquired console authority")
	}
	for _, spec := range []capability.ProvisionSpec{bootstrap, serviceSpec} {
		spec.Grants = append(slices.Clone(spec.Grants), capability.OperatorConsole)
		if issued, err := s.AsOperator().ProvisionCapability(ctx, spec); err == nil || issued.Raw != "" {
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
	if _, err := s.AsOperator().LookupCapability(ctx, operator.Capability.Digest); err == nil {
		return errors.New("revoked operator remains live")
	}
	if _, err := s.AsOperator().LookupCapability(ctx, service.Capability.Digest); err != nil {
		return fmt.Errorf("service root inherited its authorizer's revocation: %w", err)
	}
	serviceRef := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: service.Capability.Prefix}
	serviceActor := store.Actor{Authority: serviceRef, CapabilityPrefix: serviceRef.ID}
	if err := s.AsOperator().RevokeAuthority(ctx, serviceActor, serviceRef); err != nil {
		return fmt.Errorf("revoke service: %w", err)
	}
	if _, err := s.AsOperator().LookupCapability(ctx, service.Capability.Digest); err == nil {
		return errors.New("revoked service remains live")
	}
	if ready, err := s.AsOperator().HasDeploymentAdministrator(ctx); err != nil || ready {
		return fmt.Errorf("ended administrator readiness: %t, %v", ready, err)
	}
	if issued, err := s.AsOperator().ProvisionCapability(ctx, bootstrap); err == nil || issued.Raw != "" {
		return errors.New("ended bootstrap seed reopened issuance")
	}
	return nil
}

func TestStoreProvisioningOrigins(t *testing.T) {
	if err := checkStoreProvisioningOrigins(t); err != nil {
		t.Fatal(err)
	}
}

func acceptanceBootstrap(ctx context.Context, s *store.Store) (capability.Issued, error) {
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	_, err := s.AsOperator().EnsureBootstrapCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Seed: raw, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}})
	if err != nil {
		return capability.Issued{}, err
	}
	principal, err := s.AsOperator().LookupCapability(ctx, sha256.Sum256([]byte(raw)))
	return capability.Issued{Raw: raw, Capability: principal.Capability}, err
}

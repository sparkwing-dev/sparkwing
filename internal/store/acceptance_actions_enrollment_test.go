package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

type storeActionsProof struct{}

func (storeActionsProof) AuthorizeProvision(_ context.Context, _ capability.AuthorityResolver, spec capability.ProvisionSpec) error {
	if spec.Kind != capability.ProvisionActions || spec.RegistrationID != "github:42:7" {
		return errors.New("unverified Actions proof")
	}
	return nil
}

func checkStoreActionsEnrollment(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		return err
	}
	s.SetSessionKeyCipher(cipher)
	s.SetProvisionPolicy(storeActionsProof{})
	member, err := s.ResolveSignIn(ctx, googleProfile("actions-owner", "actions-owner@example.test"), store.SignUpConditions{}, time.Now())
	if err != nil {
		return err
	}
	team := member.PersonalTeam
	raw, _, _, err := s.CreateAccountSession(ctx, member.Account, team, time.Hour)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(raw))
	a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}}, Target: capability.Target{Team: string(team)}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
	tenant, err := s.ForTeam(ctx, team)
	if err != nil {
		return err
	}
	row, err := tenant.RegisterGitHubRunner(ctx, a, store.GitHubRunnerBinding{RepositoryID: 42, RepositoryOwnerID: 7, Repository: "acme/widgets"})
	if err != nil {
		return err
	}
	spec := capability.ProvisionSpec{Kind: capability.ProvisionActions, Team: string(team), RegistrationID: "github:42:7", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "github:42:7:1000:1"}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:42", Ref: "refs/heads/main", SHA: "commit", WorkflowRunID: "1000", WorkflowRunAttempt: "1", WorkflowRef: "Acme/Widgets/.github/workflows/sparkwing.yaml@refs/heads/main"}}, Repositories: []string{"gh:42"}, ExpiresAt: new(time.Now().Add(30 * time.Minute))}
	issued, err := s.ProvisionCapability(ctx, spec)
	if err != nil {
		return err
	}
	child, err := s.DelegateCapability(ctx, capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim}, Binding: spec.Binding, Repositories: spec.Repositories, ExpiresAt: spec.ExpiresAt})
	if err != nil {
		return err
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := target.TryOpen()
	if err != nil {
		return err
	}
	s = reopened
	s.SetSessionKeyCipher(cipher)
	s.SetProvisionPolicy(storeActionsProof{})
	tenant, err = s.ForTeam(ctx, team)
	if err != nil {
		return err
	}
	got, err := tenant.GitHubRunnerBinding(ctx, 42)
	if err != nil || got != row {
		return errors.Join(errors.New("reopening changed repository enrollment"), err)
	}
	for _, issued := range []capability.Issued{issued, child} {
		if _, err := s.LookupCapability(ctx, issued.Capability.Digest); err != nil {
			return err
		}
	}
	if err := tenant.RevokeGitHubRunner(ctx, a, 42); err != nil {
		return err
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err = target.TryOpen()
	if err != nil {
		return err
	}
	s = reopened
	s.SetProvisionPolicy(storeActionsProof{})
	for _, issued := range []capability.Issued{issued, child} {
		_, err := s.LookupCapability(ctx, issued.Capability.Digest)
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != apierr.Unauthenticated {
			return errors.Join(errors.New("unexpected revoked Actions authority after reopening"), err)
		}
	}
	if issued, err := s.ProvisionCapability(ctx, spec); err == nil || issued.Raw != "" {
		return errors.New("removed enrollment issued after reopening")
	}
	return nil
}

func TestStoreActionsEnrollment(t *testing.T) {
	if err := checkStoreActionsEnrollment(t); err != nil {
		t.Fatal(err)
	}
}

package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func checkStoreCapabilityManagement(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	member := signIn(t, s, "member", "member@example.test")
	team := member.PersonalTeam
	raw, _, _, err := s.AsOperator().CreateAccountSession(ctx, member.Account, team, time.Hour)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(raw))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &authority, Team: string(team), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: member.Account.ID}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
	if err != nil {
		return err
	}
	if issued.Capability.Parent == nil || *issued.Capability.Parent != authority {
		return errors.New("member credential lost its issuing session ancestry")
	}
	child, err := s.AsOperator().DelegateCapability(ctx, capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, Audience: capability.Controller, Binding: issued.Capability.Binding, Grants: issued.Capability.Grants})
	if err != nil {
		return err
	}
	reopen := func() error {
		if err := s.Close(); err != nil {
			return err
		}
		opened, err := target.TryOpen()
		if err != nil {
			return err
		}
		s = opened
		return nil
	}
	if err := reopen(); err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, team)
	if err != nil {
		return err
	}
	a := capability.Authorized{Principal: capability.Principal{Authority: authority}, Target: capability.Target{Team: string(team)}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
	rows, err := tenant.ListCapabilities(ctx, a, capability.InventoryFilter{})
	if err != nil {
		return err
	}
	if len(rows) != 2 {
		return errors.New("reopening lost owned root or delegated credential")
	}
	a.Target.TokenPrefix = issued.Capability.Prefix
	a.Access.Binding = capability.BindOwnToken
	rotation, err := tenant.RotateCapability(ctx, a, time.Hour, nil)
	if err != nil {
		return err
	}
	if rotation.Issued.Raw == "" || rotation.Previous.CreatedBy != member.Account.ID || rotation.Issued.Capability.Parent == nil || *rotation.Issued.Capability.Parent != authority {
		return errors.New("rotation lost original ownership or ancestry")
	}
	if err := reopen(); err != nil {
		return err
	}
	tenant, err = s.ForTeam(ctx, team)
	if err != nil {
		return err
	}
	for _, c := range []capability.Capability{issued.Capability, child.Capability, rotation.Issued.Capability} {
		if _, err := s.AsOperator().LookupCapability(ctx, c.Digest); err != nil {
			return err
		}
	}
	a.Target.TokenPrefix = rotation.Issued.Capability.Prefix
	next, err := tenant.RotateCapability(ctx, a, time.Hour, nil)
	if err != nil {
		return err
	}
	if next.Issued.Raw == "" || next.Issued.Capability.Parent == nil || *next.Issued.Capability.Parent != authority {
		return errors.New("reopening lost root provisioning origin")
	}
	if err := tenant.RevokeAuthority(ctx, runstate.Actor{Authority: authority}, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}); err != nil {
		return err
	}
	for _, c := range []capability.Capability{issued.Capability, child.Capability} {
		if _, err := s.AsOperator().LookupCapability(ctx, c.Digest); err == nil {
			return errors.New("explicit revocation retained predecessor ancestry")
		}
	}
	if _, err := s.AsOperator().LookupCapability(ctx, next.Issued.Capability.Digest); err != nil {
		return err
	}
	return nil
}

func TestStoreCapabilityManagement(t *testing.T) {
	if err := checkStoreCapabilityManagement(t); err != nil {
		t.Fatal(err)
	}
}

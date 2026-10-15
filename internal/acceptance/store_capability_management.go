package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CheckStoreCapabilityManagement verifies credential ownership and rotation across reopening.
func CheckStoreCapabilityManagement(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "capabilities.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		return err
	}
	s.SetSessionKeyCipher(cipher)
	for _, query := range []string{`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','member','editor',1)`} {
		if _, err := s.DB().ExecContext(ctx, query); err != nil {
			return err
		}
	}
	raw, _, _, err := s.CreateAccountSession(ctx, store.Account{ID: "member"}, store.DefaultTeam, time.Hour)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(raw))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := s.ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &authority, Team: "default", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
	if err != nil {
		return err
	}
	child, err := s.DelegateCapability(ctx, capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, Audience: capability.Controller, Binding: issued.Capability.Binding, Grants: issued.Capability.Grants})
	if err != nil {
		return err
	}
	reopen := func() error {
		if err := s.Close(); err != nil {
			return err
		}
		opened, err := store.Open(path)
		if err != nil {
			return err
		}
		s = opened
		s.SetSessionKeyCipher(cipher)
		return nil
	}
	if err := reopen(); err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	a := capability.Authorized{Principal: capability.Principal{Authority: authority}, Target: capability.Target{Team: "default"}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
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
	if rotation.Issued.Raw == "" || rotation.Previous.CreatedBy != "member" || rotation.Issued.Capability.Parent != nil {
		return errors.New("rotation lost original ownership or ancestry")
	}
	if err := reopen(); err != nil {
		return err
	}
	tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	for _, c := range []capability.Capability{issued.Capability, child.Capability, rotation.Issued.Capability} {
		if _, err := s.LookupCapability(ctx, c.Digest); err != nil {
			return err
		}
	}
	a.Target.TokenPrefix = rotation.Issued.Capability.Prefix
	next, err := tenant.RotateCapability(ctx, a, time.Hour, nil)
	if err != nil {
		return err
	}
	if next.Issued.Raw == "" {
		return errors.New("reopening lost root provisioning origin")
	}
	if err := tenant.RevokeAuthority(ctx, store.Actor{Authority: authority}, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}); err != nil {
		return err
	}
	for _, c := range []capability.Capability{issued.Capability, child.Capability} {
		if _, err := s.LookupCapability(ctx, c.Digest); err == nil {
			return errors.New("explicit revocation retained predecessor ancestry")
		}
	}
	if _, err := s.LookupCapability(ctx, next.Issued.Capability.Digest); err != nil {
		return err
	}
	return nil
}

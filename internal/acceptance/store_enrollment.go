package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CheckStoreEnrolledClaims verifies persisted enrollment and repository matching through reopening.
func CheckStoreEnrolledClaims(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "enrollment.db")
	f, err := newStoreRecoveryFixture(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	f.s.SetClaimProfileResolver(nil)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		return err
	}
	f.s.SetSessionKeyCipher(cipher)
	if _, err := f.s.DB().ExecContext(ctx, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('registrar','registrar@example.test',1,1)`); err != nil {
		return err
	}
	if _, err := f.s.DB().ExecContext(ctx, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','registrar','owner',1)`); err != nil {
		return err
	}
	raw, _, _, err := f.s.CreateAccountSession(ctx, store.Account{ID: "registrar"}, store.DefaultTeam, time.Hour)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(raw))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	spec := capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: "default", RegistrationID: "runner:default:enrolled", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "enrolled"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, ReservedPools: []string{"private"}}
	issued, err := f.s.ProvisionCapability(ctx, spec)
	if err != nil {
		return err
	}
	if again, err := f.s.ProvisionCapability(ctx, spec); !errors.Is(err, store.ErrAgentNameTaken) || again.Raw != "" {
		return errors.Join(errors.New("active enrollment name was reused"), err)
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE nodes SET claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,ready_at=1,execution_json=?`, []byte(`{"requires":{"set":true,"value":["pool=private","name=enrolled"]}}`)); err != nil {
		return err
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE runs SET allowed_pools=?`, []byte(`["private"]`)); err != nil {
		return err
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE triggers SET expires_at=?`, time.Now().Add(time.Hour).UnixNano()); err != nil {
		return err
	}
	if err := f.reopen(ctx, path); err != nil {
		return err
	}
	f.s.SetClaimProfileResolver(nil)
	profile, _, err := f.s.ReadClaimProfile(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix})
	if err != nil || profile.Name != "enrolled" || profile.Class != match.ClassAgent || len(profile.ReservedPools) != 1 || profile.Accept == nil {
		return errors.Join(errors.New("reopening lost enrolled identity"), err)
	}
	demand := store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}, Labels: []string{"name=forged", "pool=forged", "class=cloud"}}
	for _, repository := range []string{"foreign", "acme"} {
		request := runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:42", Display: "https://github.com/" + repository + "/repo"}}}}
		body, err := json.Marshal(request)
		if err != nil {
			return err
		}
		if _, err := f.s.DB().ExecContext(ctx, `UPDATE runs SET repo_key='gh:42',request_json=?`, body); err != nil {
			return err
		}
		claim, err := f.s.ClaimUnit(ctx, demand)
		if repository == "foreign" {
			if !errors.Is(err, store.ErrNotFound) || len(claim.Bearer) != 0 {
				return errors.Join(errors.New("enrollment permitted another repository"), err)
			}
			continue
		}
		if err != nil || claim.Team != store.DefaultTeam || claim.Award.Holder != "enrolled" || len(claim.Bearer) == 0 {
			return errors.Join(errors.New("enrolled machine did not receive its award"), err)
		}
		if err := f.reopen(ctx, path); err != nil {
			return err
		}
		persisted, err := f.s.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
		if err != nil || persisted.Capability.Parent == nil || persisted.Capability.Parent.ID != issued.Capability.Prefix || len(persisted.Capability.ReservedPools) != 1 {
			return errors.Join(errors.New("award lost registered ancestry or pools"), err)
		}
	}
	return nil
}

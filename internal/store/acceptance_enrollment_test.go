package store_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func checkStoreEnrolledClaims(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	_, plan, err := storePlanSealRequest(ctx, s)
	if err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.Team(plan.Seal.Request.Request.Team))
	if err != nil {
		return err
	}
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		return err
	}
	team := tenant.Team()
	authority := plan.Seal.Request.AuthorityRef
	reopen := func() error {
		if err := s.Close(); err != nil {
			return err
		}
		var err error
		s, err = target.TryOpen()
		return err
	}
	spec := capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(team), RegistrationID: "runner:" + string(team) + ":enrolled", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "enrolled"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, ReservedPools: []string{"private"}}
	issued, err := s.AsOperator().ProvisionCapability(ctx, spec)
	if err != nil {
		return err
	}
	if again, err := s.AsOperator().ProvisionCapability(ctx, spec); !errors.Is(err, store.ErrAgentNameTaken) || again.Raw != "" {
		return errors.Join(errors.New("active enrollment name was reused"), err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["pool=private","name=enrolled"]}}`)); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE runs SET allowed_pools=?`, []byte(`["private"]`)); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE triggers SET expires_at=?`, time.Now().Add(time.Hour).UnixNano()); err != nil {
		return err
	}
	if _, err := tenant.TransitionNode(ctx, store.NodeTransition{Ref: controllerwire.NodeRef{RunID: plan.RunID, NodeID: "alpha"}, Actor: plan.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
		return err
	}
	if err := reopen(); err != nil {
		return err
	}
	profile, _, err := s.AsOperator().ReadClaimProfile(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix})
	if err != nil || profile.Name != "enrolled" || profile.Class != match.ClassAgent || len(profile.ReservedPools) != 1 || profile.Accept == nil {
		return errors.Join(errors.New("reopening lost enrolled identity"), err)
	}
	demand := store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}, Labels: []string{"name=forged", "pool=forged", "class=cloud"}}
	var admitted []byte
	if err := s.DB().QueryRowContext(ctx, `SELECT request_json FROM runs`).Scan(&admitted); err != nil {
		return err
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(admitted, &sealed); err != nil {
		return err
	}
	for _, repository := range []string{"foreign", "acme"} {
		request := runspec.SealedRequest{Provenance: sealed.Provenance, Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:42", Display: "https://github.com/" + repository + "/repo"}}}}
		body, err := json.Marshal(request)
		if err != nil {
			return err
		}
		if _, err := s.DB().ExecContext(ctx, `UPDATE runs SET repo_key='gh:42',request_json=?,repo_display=?`, body, request.Request.Git.Repository.Display); err != nil {
			return err
		}
		claim, err := s.AsOperator().ClaimUnit(ctx, demand)
		if repository == "foreign" {
			if !errors.Is(err, store.ErrNotFound) || len(claim.Bearer) != 0 {
				return errors.Join(errors.New("enrollment permitted another repository"), err)
			}
			continue
		}
		if err != nil || claim.Team != team || claim.Award.Holder != "enrolled" || len(claim.Bearer) == 0 {
			return errors.Join(errors.New("enrolled machine did not receive its award"), err)
		}
		if err := reopen(); err != nil {
			return err
		}
		persisted, err := s.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
		if err != nil || persisted.Capability.Parent == nil || persisted.Capability.Parent.ID != issued.Capability.Prefix || len(persisted.Capability.ReservedPools) != 1 {
			return errors.Join(errors.New("award lost registered ancestry or pools"), err)
		}
	}
	return nil
}

func TestStoreEnrolledClaims(t *testing.T) {
	if err := checkStoreEnrolledClaims(t); err != nil {
		t.Fatal(err)
	}
}

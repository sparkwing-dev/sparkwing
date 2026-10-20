package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func provisionOrigin(t *testing.T, kind capability.ProvisionKind) (*Store, capability.ProvisionSpec) {
	t.Helper()
	s := authorityStore(t)
	expires := s.now().Add(time.Hour)
	spec := capability.ProvisionSpec{Kind: kind, RegistrationID: "enrollment", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "machine"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: &expires}
	switch kind {
	case capability.ProvisionBootstrap:
		spec = bootstrapSpec([32]byte{1})
	case capability.ProvisionMachine:
		owner := authorityMember(t, s, "owner", RoleOwner)
		spec.Authority = &owner.Authority
		spec.RegistrationID = "runner:a:machine"
		spec.Repositories = []string{"github.com/acme/*"}
	case capability.ProvisionService:
		operator := authorityRoot("operator", "")
		operator.Subject.Kind = capability.SubjectOperator
		operator.Grants = []capability.Grant{capability.OperatorTokens}
		authorityPersist(t, s, operator)
		spec.Authority = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Prefix}
		spec.Team, spec.Subject = "", capability.Subject{Kind: capability.SubjectService, ID: "launcher"}
		spec.Grants = []capability.Grant{capability.ClaimsLaunch}
		spec.Metered = true
		spec.RegistrationID = "service:launcher"
	case capability.ProvisionActions:
		rewriteSchemaExec(t, s.db, `INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,repo_key,created_at) VALUES ('a',42,7,'acme/widgets','gh:42',1)`)
		spec = actionsSpec(t, s, "a", "workflow")
	}
	return s, spec
}

func TestProvisionOriginsUseNativeVerification(t *testing.T) {
	for _, kind := range []capability.ProvisionKind{capability.ProvisionBootstrap, capability.ProvisionMachine, capability.ProvisionService, capability.ProvisionActions} {
		t.Run(string(kind), func(t *testing.T) {
			s, spec := provisionOrigin(t, kind)
			issued, err := s.ProvisionCapability(t.Context(), spec)
			if err != nil || issued.Raw == "" || issued.Capability.Parent != nil {
				t.Fatalf("native origin: %+v, %v", issued.Capability, err)
			}
			if _, err := s.LookupCapability(t.Context(), issued.Capability.Digest); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProvisionMachineRecordsAccountOwner(t *testing.T) {
	s, spec := provisionOrigin(t, capability.ProvisionMachine)
	issued, err := s.ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := s.db.QueryRow(`SELECT created_by FROM capabilities WHERE prefix=?`, issued.Capability.Prefix).Scan(&owner); err != nil || owner == "" {
		t.Fatalf("machine owner = %q: %v", owner, err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=1 WHERE hash=?`, spec.Authority.ID)
	if _, err := s.LookupCapability(t.Context(), issued.Capability.Digest); err != nil {
		t.Fatalf("independent enrolled root inherited the authorizer's lifetime: %v", err)
	}
}

func TestProvisionRefusesEndedAuthorizer(t *testing.T) {
	for _, problem := range []string{"revoked", "membership", "expired"} {
		t.Run(problem, func(t *testing.T) {
			s, spec := provisionOrigin(t, capability.ProvisionMachine)
			switch problem {
			case "revoked":
				rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=1 WHERE hash=?`, spec.Authority.ID)
			case "membership":
				rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE team='a' AND account_id='owner'`)
			case "expired":
				s.clock = func() time.Time { return time.Now().Add(2 * time.Hour) }
			}
			issued, err := s.ProvisionCapability(t.Context(), spec)
			if err == nil || issued.Raw != "" {
				t.Fatal("ended authorizer minted a bearer")
			}
			rewriteSchemaCount(t, s.db, "capabilities", 0)
		})
	}
}

func TestProvisionOriginAuthorityProperty(t *testing.T) {
	s, base := provisionOrigin(t, capability.ProvisionMachine)
	random := rand.New(rand.NewPCG(42, 91))
	for i := range 128 {
		spec := base
		spec.Subject.ID = fmt.Sprintf("machine-%d", i)
		spec.Grants = append([]capability.Grant(nil), base.Grants...)
		switch random.IntN(8) {
		case 0:
			spec.Team = "b"
		case 1:
			spec.Metered = true
		case 2:
			spec.Grants = append(spec.Grants, capability.OperatorTokens)
		case 3:
			spec.Grants = append(spec.Grants, capability.ClaimsLaunch)
		case 4:
			spec.Grants = append(spec.Grants, capability.OperatorConsole)
		case 5:
			spec.Binding = capability.Binding{Kind: capability.BindingRun, RunID: fmt.Sprintf("run-%d", random.Uint64())}
		case 6:
			spec.Subject.Kind = capability.SubjectService
		case 7:
			spec.Authority = nil
		}
		issued, err := s.ProvisionCapability(t.Context(), spec)
		if err == nil || issued.Raw != "" {
			t.Fatalf("forged root %d minted authority: %+v", i, spec)
		}
	}
	rewriteSchemaCount(t, s.db, "capabilities", 0)
}

func TestProvisionRootCannotAcquireAnExecutionFence(t *testing.T) {
	for _, kind := range []capability.ProvisionKind{capability.ProvisionBootstrap, capability.ProvisionMachine, capability.ProvisionService, capability.ProvisionActions} {
		t.Run(string(kind), func(t *testing.T) {
			s, base := provisionOrigin(t, kind)
			random := rand.New(rand.NewPCG(17, 83))
			for range 64 {
				spec := base
				switch random.IntN(8) {
				case 0:
					spec.Binding.RunID = fmt.Sprintf("run-%d", random.Uint64())
				case 1:
					spec.Binding.NodeID = fmt.Sprintf("job-%d", random.Uint64())
				case 2:
					spec.Binding.Holder = fmt.Sprintf("holder-%d", random.Uint64())
				case 3:
					spec.Binding.Generation = random.Int64N(1000) + 1
				case 4:
					spec.Binding.Attempt = random.Int64N(1000) + 1
				case 5:
					spec.Binding.Reservation = fmt.Sprintf("reservation-%d", random.Uint64())
				case 6:
					spec.Binding.ClaimBy = new(s.now())
				case 7:
					spec.Binding.Deadline = new(s.now().Add(time.Hour))
				}
				issued, err := s.ProvisionCapability(t.Context(), spec)
				if err == nil || issued.Raw != "" {
					t.Fatalf("root acquired forged execution fence: %+v", spec.Binding)
				}
			}
			want := 0
			if kind == capability.ProvisionService {
				want = 1
			}
			rewriteSchemaCount(t, s.db, "capabilities", want)
		})
	}
}

func TestProvisionRejectsUnregisteredAndDeletingTeams(t *testing.T) {
	for _, problem := range []string{"missing", "deleting"} {
		t.Run(problem, func(t *testing.T) {
			s, spec := provisionOrigin(t, capability.ProvisionActions)
			if problem == "missing" {
				spec.Team = "missing"
			} else {
				rewriteSchemaExec(t, s.db, `INSERT INTO team_deletions(slug,requested_by,requested_at,state) VALUES ('a','owner',1,?)`, TeamDeletionPending)
			}
			issued, err := s.ProvisionCapability(t.Context(), spec)
			if !errors.Is(err, ErrUnknownTeam) || issued.Raw != "" {
				t.Fatalf("provisioned unregistered team: %+v, %v", issued.Capability, err)
			}
			rewriteSchemaCount(t, s.db, "capabilities", 0)
		})
	}
}

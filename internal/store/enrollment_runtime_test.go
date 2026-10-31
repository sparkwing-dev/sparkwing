package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func machineEnrollment(t *testing.T) (*Store, capability.ProvisionSpec) {
	t.Helper()
	s, spec := provisionOrigin(t, capability.ProvisionMachine)
	spec.RegistrationID = "runner:a:machine"
	spec.Repositories = []string{"github.com/acme/*"}
	spec.ReservedPools = []string{"private"}
	return s, spec
}

func TestMachineEnrollmentUsesStoreOrigin(t *testing.T) {
	s, spec := machineEnrollment(t)
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil || issued.Raw == "" || issued.Capability.Parent != nil {
		t.Fatalf("enrollment: %+v, %v", issued.Capability, err)
	}
	p, err := s.AsOperator().ReadClaimProfile(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix})
	if err != nil || p.Name != "machine" || p.Class != "agent" || len(p.ReservedPools) != 1 || p.Accept == nil {
		t.Fatalf("profile: %+v, %v", p, err)
	}
	_, allowed := p.Accept.AdmitsRepository("https://github.com/acme/widgets.git", "", "", "")
	_, denied := p.Accept.AdmitsRepository("https://github.com/other/widgets.git", "", "", "")
	if !allowed || denied {
		t.Fatal("profile did not retain repository enrollment")
	}
	if again, err := s.AsOperator().ProvisionCapability(t.Context(), spec); !errors.Is(err, ErrAgentNameTaken) || again.Raw != "" {
		t.Fatalf("duplicate: %+v, %v", again.Capability, err)
	}
	actor := runstate.Actor{Authority: *spec.Authority}
	a := inventoryAuthorization(t, s, actor, "a", issued.Capability.Prefix)
	rotation, err := (&Tenant{s: s, team: "a"}).RotateCapability(t.Context(), a, time.Hour, nil)
	if err != nil || !sameRotationScope(issued.Capability, rotation.Issued.Capability) {
		t.Fatalf("rotation: %+v, %v", rotation, err)
	}
	if again, err := s.AsOperator().ProvisionCapability(t.Context(), spec); !errors.Is(err, ErrAgentNameTaken) || again.Raw != "" {
		t.Fatalf("rotation freed active name: %+v, %v", again.Capability, err)
	}
}

func TestUnscopedEnrollmentIsPersonalOnly(t *testing.T) {
	for _, personal := range []bool{false, true} {
		t.Run(fmt.Sprint(personal), func(t *testing.T) {
			s, spec := machineEnrollment(t)
			spec.Personal, spec.Repositories, spec.ReservedPools = personal, []string{}, nil
			issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
			if !personal {
				if err == nil || issued.Raw != "" {
					t.Fatal("shared enrollment accepted an absent repository scope")
				}
				return
			}
			if err != nil || issued.Capability.Repositories != nil {
				t.Fatalf("unscoped personal enrollment retained a repository restriction: %+v %v", issued.Capability.Repositories, err)
			}
			profile, err := s.AsOperator().ReadClaimProfile(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix})
			if err != nil || profile.Accept != nil {
				t.Fatalf("unscoped personal profile applied a repository filter: %+v %v", profile, err)
			}
		})
	}
}

func TestMachineEnrollmentAuthorityProperty(t *testing.T) {
	s, base := machineEnrollment(t)
	check := func(change uint8, suffix uint16) bool {
		spec := base
		spec.Subject.ID = fmt.Sprintf("runner-%d", suffix)
		spec.RegistrationID = "runner:a:" + spec.Subject.ID
		switch change % 9 {
		case 0:
			rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='editor' WHERE account_id='owner'`)
		case 1:
			spec.RegistrationID = "runner:b:" + spec.Subject.ID
		case 2:
			spec.Repositories = nil
		case 3:
			spec.Repositories = []string{"https://github.com/acme/*"}
		case 4:
			spec.Grants = []capability.Grant{capability.NodeReport}
		case 5:
			spec.Grants = []capability.Grant{capability.NodesClaim, capability.NodesClaim}
		case 6:
			spec.ReservedPools = []string{"private", "private"}
		case 7:
			spec.Data = &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "gh:42"}
		case 8:
			spec.Subject.ID = "invalid/name"
			spec.RegistrationID = "runner:a:" + spec.Subject.ID
		}
		issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
		rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='owner' WHERE account_id='owner'`)
		return err != nil && issued.Raw == ""
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "capabilities", 0)
}

func TestMachineEnrollmentSerializesNamesAndLimits(t *testing.T) {
	for _, limit := range []bool{false, true} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			s, base := machineEnrollment(t)
			if limit {
				for i := range maxRunnerTokensPerTeam - 1 {
					spec := base
					spec.Subject.ID = fmt.Sprintf("registered-%d", i)
					spec.RegistrationID = "runner:a:" + spec.Subject.ID
					if _, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err != nil {
						t.Fatal(err)
					}
				}
			}
			var wg sync.WaitGroup
			results := make(chan error, 8)
			for i := range 8 {
				wg.Go(func() {
					spec := base
					if limit {
						spec.Subject.ID = fmt.Sprintf("contender-%d", i)
						spec.RegistrationID = "runner:a:" + spec.Subject.ID
					}
					_, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
					results <- err
				})
			}
			wg.Wait()
			close(results)
			winners := 0
			for err := range results {
				if err == nil {
					winners++
				} else if !errors.Is(err, ErrAgentNameTaken) && !errors.Is(err, ErrRunnerTokenLimit) {
					t.Fatal(err)
				}
				if errors.Is(err, ErrRunnerTokenLimit) {
					for _, want := range []string{"limit is 100", "current usage is 100", "support@sparkwing.dev"} {
						if !strings.Contains(err.Error(), want) {
							t.Fatalf("runner refusal %q omits %q", err, want)
						}
					}
				}
			}
			if winners != 1 {
				t.Fatalf("winners=%d", winners)
			}
		})
	}
}

func TestServiceEnrollmentUsesDeploymentOrigin(t *testing.T) {
	s, spec := provisionOrigin(t, capability.ProvisionService)
	spec.RegistrationID = "service:launcher"
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil || issued.Raw == "" {
		t.Fatalf("service enrollment: %+v, %v", issued.Capability, err)
	}
	if again, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err == nil || again.Raw != "" {
		t.Fatalf("duplicate service: %+v, %v", again.Capability, err)
	}
}

func TestMachineEnrollmentRollbackDoesNotReserveName(t *testing.T) {
	s, spec := machineEnrollment(t)
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_enrollment BEFORE INSERT ON capabilities BEGIN SELECT RAISE(ABORT,'refused'); END`)
	if issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err == nil || issued.Raw != "" {
		t.Fatal("failed enrollment returned a bearer")
	}
	rewriteSchemaCount(t, s.db, "capabilities", 0)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_enrollment`)
	if _, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
}

func TestMachineEnrollmentNameReuseProperty(t *testing.T) {
	check := func(revoked bool, offset int16) bool {
		s, spec := machineEnrollment(t)
		now := s.now()
		s.clock = func() time.Time { return now }
		deadline := now.Add(time.Hour)
		spec.ExpiresAt = &deadline
		old, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		if revoked {
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: old.Capability.Prefix}
			if err := (&Tenant{s: s, team: "a"}).RevokeAuthority(t.Context(), runstate.Actor{Authority: *spec.Authority}, ref); err != nil {
				t.Fatal(err)
			}
		} else {
			now = deadline.Add(time.Duration(offset % 3))
		}
		spec.ExpiresAt = new(now.Add(time.Hour))
		issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
		if revoked || !now.Before(deadline) {
			return err == nil && issued.Raw != "" && issued.Capability.Prefix != old.Capability.Prefix
		}
		return errors.Is(err, ErrAgentNameTaken) && issued.Raw == ""
	}
	if err := quick.Check(check, quicktest.Config(&quick.Config{MaxCount: 64})); err != nil {
		t.Fatal(err)
	}
}

func TestMachineEnrollmentNamesAreTeamScoped(t *testing.T) {
	s, spec := machineEnrollment(t)
	if _, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	other := authorityMember(t, s, "other", RoleOwner, "b")
	spec.Authority, spec.Team, spec.RegistrationID = &other.Authority, "b", "runner:b:machine"
	if _, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
}

func TestEnrolledClaimRepositoryAncestryProperty(t *testing.T) {
	s, spec := machineEnrollment(t)
	_, demand := unitClaimFixtureStore(t, s)
	spec.ReservedPools = nil
	root, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}, Audience: capability.Controller, Binding: spec.Binding, Grants: spec.Grants, ExpiresAt: spec.ExpiresAt, Repositories: spec.Repositories})
	if err != nil {
		t.Fatal(err)
	}
	check := func(foreign bool, suffix uint16, delegated bool) bool {
		owner := "acme"
		if foreign {
			owner = fmt.Sprintf("foreign-%d", suffix)
		}
		repository := owner + fmt.Sprintf("/repo-%d", suffix)
		provenance, err := assertedProvenance(t, "ci", "", "").FetchedFrom(1, repository, "", "")
		if err != nil {
			t.Fatal(err)
		}
		request := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/" + repository}}}}, provenance)
		rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,repo_display=?`, request, "https://github.com/"+repository)
		demand.CapabilityPrefix = root.Capability.Prefix
		if delegated {
			demand.CapabilityPrefix = child.Capability.Prefix
		}
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if foreign {
			return err != nil && len(claim.Bearer) == 0
		}
		if err != nil || claim.Award.Holder != "machine" {
			t.Logf("claim: %+v, %v", claim, err)
			return false
		}
		if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=1`)
		return true
	}
	if err := quick.Check(check, quicktest.Config(&quick.Config{MaxCount: 80})); err != nil {
		t.Fatal(err)
	}
}

func TestRotationKeepsRunnerTrustAndPlatform(t *testing.T) {
	s, spec := machineEnrollment(t)
	spec.Trust, spec.Platform = capability.TrustAny, "kubernetes"
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	a := inventoryAuthorization(t, s, runstate.Actor{Authority: *spec.Authority}, "a", issued.Capability.Prefix)
	rotation, err := (&Tenant{s: s, team: "a"}).RotateCapability(t.Context(), a, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	successor, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: rotation.Issued.Capability.Prefix})
	if err != nil || successor.Capability.Trust != capability.TrustAny {
		t.Fatalf("successor trust = %q, %v", successor.Capability.Trust, err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		chain, err := authorityChainTx(t.Context(), tx, successor.Authority, false)
		if err != nil {
			return err
		}
		facts, err := claimantFactsTx(t.Context(), tx, chain, runspec.Request{})
		if err == nil && !facts.PersistentKubernetes {
			t.Error("rotation lost the enrolled Kubernetes platform")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	changed := issued.Capability
	changed.Trust = capability.TrustVerifiedDefault
	if sameRotationScope(issued.Capability, changed) {
		t.Fatal("a successor with another trust class counts as the same rotation scope")
	}
}

func TestRunnerPlatformAllowsOnlySharedKubernetesEnrollment(t *testing.T) {
	for _, platform := range []string{"kubernetes", "other"} {
		for _, personal := range []bool{false, true} {
			t.Run(fmt.Sprint(platform, personal), func(t *testing.T) {
				s, spec := machineEnrollment(t)
				spec.Platform, spec.Personal = platform, personal
				_, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
				if (err == nil) != (platform == "kubernetes" && !personal) {
					t.Fatalf("platform=%q personal=%v enrollment: %v", platform, personal, err)
				}
			})
		}
	}
}

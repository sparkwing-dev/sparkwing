package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func bootstrapSpec(secret [32]byte) capability.ProvisionSpec {
	return capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Seed: capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(secret[:]), Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}}
}

func TestBootstrapSeedIsOnceOnlyProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(93, 715))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := authorityStore(t)
			var secret [32]byte
			for j := range secret {
				secret[j] = byte(random.Uint32())
			}
			spec := bootstrapSpec(secret)
			created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec)
			if err != nil || !created {
				t.Fatalf("bootstrap: %t %v", created, err)
			}
			digest := sha256.Sum256([]byte(spec.Seed))
			p, err := s.AsOperator().LookupCapability(t.Context(), digest)
			if err != nil || p.Capability.Prefix != spec.Seed[:16] || p.Capability.Parent != nil {
				t.Fatalf("seed authority: %+v %v", p, err)
			}
			var body []byte
			if err := s.db.QueryRow(`SELECT authority_json FROM capabilities WHERE prefix=?`, p.Capability.Prefix).Scan(&body); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(body, []byte(spec.Seed)) {
				t.Fatal("persisted the raw seed")
			}
			if created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec); err != nil || created {
				t.Fatalf("live replay: %t %v", created, err)
			}
			other := spec
			secret[(i%2)*31] ^= 255
			other.Seed = bootstrapSpec(secret).Seed
			if created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), other); err == nil || created {
				t.Fatal("a different seed bypassed the gate")
			}
			if err := s.AsOperator().RevokeAuthority(t.Context(), runstate.Actor{Authority: p.Authority}, p.Authority); err != nil {
				t.Fatal(err)
			}
			if created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec); err == nil || created {
				t.Fatal("revived a revoked seed")
			}
			if live, err := s.AsOperator().HasDeploymentAdministrator(t.Context()); err != nil || live {
				t.Fatalf("revoked administrator: %t %v", live, err)
			}
			rewriteSchemaExec(t, s.db, `DELETE FROM capabilities`)
			if _, err := s.AsOperator().ProvisionCapability(t.Context(), other); err == nil {
				t.Fatal("retention reopened the bootstrap gate")
			}
			rewriteSchemaCount(t, s.db, "capabilities", 0)
		})
	}
}

func TestBootstrapSeedCannotEnterOtherOriginsProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(71, 99))
	for _, kind := range []capability.ProvisionKind{capability.ProvisionMember, capability.ProvisionMachine, capability.ProvisionService, capability.ProvisionActions} {
		t.Run(string(kind), func(t *testing.T) {
			s, spec := provisionOrigin(t, kind)
			if kind == capability.ProvisionMember {
				member := authorityMember(t, s, "owner", RoleOwner)
				spec = capability.ProvisionSpec{Kind: kind, Authority: &member.Authority, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}, Team: "a", Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}}
			}
			for range 16 {
				var secret [32]byte
				for j := range secret {
					secret[j] = byte(random.Uint32())
				}
				spec.Seed = bootstrapSpec(secret).Seed
				issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
				if err == nil || issued.Raw != "" {
					t.Fatal("non-bootstrap origin accepted a seed")
				}
			}
		})
	}
}

func TestBootstrapGateSurvivesConcurrentReplicasAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	spec := bootstrapSpec([32]byte{42})
	var wg sync.WaitGroup
	results := make(chan bool, 12)
	failures := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() {
			s := first
			if i%2 == 1 {
				s = second
			}
			created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec)
			results <- created
			failures <- err
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	count := 0
	for created := range results {
		if created {
			count++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("created %d administrators", count)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if created, err := reopened.AsOperator().EnsureBootstrapCapability(t.Context(), spec); err != nil || created {
		t.Fatalf("restart replay: %t %v", created, err)
	}
	if live, err := reopened.AsOperator().HasDeploymentAdministrator(t.Context()); err != nil || !live {
		t.Fatalf("restarted administrator: %t %v", live, err)
	}
}

func TestBootstrapFailureRollsBackGateAndBearer(t *testing.T) {
	for name, trigger := range map[string]string{
		"capability":         `CREATE TRIGGER refuse_bootstrap BEFORE INSERT ON capabilities BEGIN SELECT RAISE(ABORT,'refused'); END`,
		"gate":               `CREATE TRIGGER refuse_bootstrap BEFORE UPDATE ON settings WHEN NEW.key='bootstrap_admin' AND LENGTH(NEW.value)>0 BEGIN SELECT RAISE(ABORT,'refused'); END`,
		"missing gate write": `CREATE TRIGGER refuse_bootstrap BEFORE UPDATE ON settings WHEN NEW.key='bootstrap_admin' AND LENGTH(NEW.value)>0 BEGIN SELECT RAISE(IGNORE); END`,
	} {
		t.Run(name, func(t *testing.T) {
			s := authorityStore(t)
			spec := bootstrapSpec([32]byte{7})
			rewriteSchemaExec(t, s.db, trigger)
			if created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec); err == nil || created {
				t.Fatal("failed commit returned authority")
			}
			rewriteSchemaCount(t, s.db, "capabilities", 0)
			rewriteSchemaCount(t, s.db, "settings", 0)
			rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_bootstrap`)
			if created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec); err != nil || !created {
				t.Fatalf("rolled back gate: %t %v", created, err)
			}
		})
	}
}

func TestDeploymentAdministratorRequiresLiveAncestryProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(217, 45))
	for _, problem := range []string{"live", "team", "grant", "bound", "expired", "future", "deadline", "revoked", "ancestor"} {
		t.Run(problem, func(t *testing.T) {
			s := authorityStore(t)
			now := time.Unix(1700000000+random.Int64N(1000), 0).UTC()
			s.clock = func() time.Time { return now }
			admin := authorityRoot("administrator", "")
			admin.Subject.Kind = capability.SubjectOperator
			admin.Grants = []capability.Grant{capability.OperatorTokens}
			switch problem {
			case "team":
				admin.Team = "a"
			case "grant":
				admin.Grants = []capability.Grant{capability.OperatorLimits}
			case "bound":
				unitClaimFixtureStore(t, s)
				admin.Team = "a"
				admin.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
			case "expired":
				admin.ExpiresAt = &now
			case "future":
				admin.IssuedAt = now.Add(time.Second)
			case "deadline":
				admin.Binding.Deadline = &now
			case "revoked":
				admin.RevokedAt = &now
			case "ancestor":
				parent := authorityRoot("parent", "a")
				parent.RevokedAt = &now
				authorityPersist(t, s, parent)
				admin.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Prefix}
			}
			authorityPersist(t, s, admin)
			if live, err := s.AsOperator().HasDeploymentAdministrator(t.Context()); err != nil || live != (problem == "live") {
				t.Fatalf("administrator %s: %t %v", problem, live, err)
			}
			if problem == "live" {
				spec := bootstrapSpec([32]byte{9})
				if created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec); err == nil || created {
					t.Fatal("bootstrap bypassed existing administrator")
				}
				rewriteSchemaCount(t, s.db, "capabilities", 1)
			}
		})
	}
}

func TestIssueRejectsMalformedBootstrapPresentations(t *testing.T) {
	s := authorityStore(t)
	for _, raw := range []string{"swu_private", "swn_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "swk_private\n", "swk_" + base64.RawURLEncoding.EncodeToString(make([]byte, 31)), "swk_" + base64.RawURLEncoding.EncodeToString(make([]byte, 33))} {
		spec := bootstrapSpec([32]byte{})
		spec.Seed = raw
		if created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec); err == nil || created {
			t.Fatal("invalid presentation reached the bootstrap gate")
		}
		if issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err == nil || issued.Raw != "" {
			t.Fatal("direct Issue accepted an invalid seed")
		}
	}
	rewriteSchemaCount(t, s.db, "capabilities", 0)
	rewriteSchemaCount(t, s.db, "settings", 0)
}

func TestBootstrapDifferentSeedsRaceForOneGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var wg sync.WaitGroup
	results := make(chan bool, 12)
	failures := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() {
			s := first
			if i%2 == 1 {
				s = second
			}
			created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), bootstrapSpec([32]byte{byte(i + 1)}))
			var api *apierr.Error
			if err != nil && (!errors.As(err, &api) || api.Code != apierr.Conflict) {
				failures <- err
			}
			results <- created
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	count := 0
	for created := range results {
		if created {
			count++
		}
	}
	for err := range failures {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("created %d administrators", count)
	}
	rewriteSchemaCount(t, first.db, "capabilities", 1)
}

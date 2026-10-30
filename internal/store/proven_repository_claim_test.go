package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func proveClaimFixtureRepository(t *testing.T, s *Store) {
	t.Helper()
	var raw []byte
	if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE team='a' AND id='run'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(raw, &sealed); err != nil {
		t.Fatal(err)
	}
	p, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "owner"), "refs/heads/main", rankingSHA, "ci")
	if err != nil {
		t.Fatal(err)
	}
	sealed.Provenance, err = p.FetchedFrom(1, "acme/repo", "refs/heads/main", rankingSHA)
	if err != nil {
		t.Fatal(err)
	}
	sealed.Request.Git.Ref, sealed.Request.Git.SHA = "refs/heads/main", rankingSHA
	raw, err = json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,created_principal='owner',created_subject_kind='account' WHERE team='a' AND id='run'`, raw)
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitTrigger, RunID: "run"})
}

func TestPersonalMachinesNeverClaimProvenRepositoryRuns(t *testing.T) {
	for _, placement := range []string{"personal", "here", "machines"} {
		for _, named := range []bool{false, true} {
			for _, proven := range []bool{false, true} {
				t.Run(placement+"/"+map[bool]string{false: "queue", true: "named"}[named]+"/"+map[bool]string{false: "asserted", true: "proven"}[proven], func(t *testing.T) {
					s, demand := unitClaimFixture(t)
					personal := authorityRoot("laptop", "a")
					authorityPersist(t, s, personal)
					setMachineOrigin(t, s, personal.Prefix, &CapabilityOrigin{Kind: capability.ProvisionMachine, Personal: true, RegistrationID: "runner:a:laptop"})
					rewriteSchemaExec(t, s.db, `UPDATE capabilities SET created_by='owner' WHERE prefix=?`, personal.Prefix)
					var raw []byte
					if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE team='a' AND id='run'`).Scan(&raw); err != nil {
						t.Fatal(err)
					}
					var sealed runspec.SealedRequest
					if err := json.Unmarshal(raw, &sealed); err != nil {
						t.Fatal(err)
					}
					if placement == "here" {
						sealed.Request.OriginMachine = "laptop"
					}
					if placement == "machines" {
						sealed.Request.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "machines"}}}
					}
					sealed.Provenance = assertedProvenance(t, "ci", "", "")
					raw, err := json.Marshal(sealed)
					if err != nil {
						t.Fatal(err)
					}
					rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,created_principal='owner',created_subject_kind='account' WHERE team='a' AND id='run'`, raw)
					fixtureRunWriter(t, s, "run", runspec.PrincipalWriter("account", "owner"))
					if proven {
						proveClaimFixtureRepository(t, s)
					}
					demand.CapabilityPrefix = personal.Prefix
					if !named {
						demand.Target = nil
						demand.Kinds = []capability.UnitKind{capability.UnitNode}
					}
					claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
					if proven {
						if err == nil {
							t.Fatalf("personal machine claimed proven repository work: %+v", claim)
						}
						var denied *apierr.Error
						if named && (!errors.As(err, &denied) || denied.Code != apierr.Forbidden || denied.Message != provenRepositoryHostReason) {
							t.Fatalf("named claim hid host trust refusal: %v", err)
						}
						return
					}
					if err != nil || claim.NodeID != "job" {
						t.Fatalf("owner's asserted work was refused: %+v %v", claim, err)
					}
				})
			}
		}
	}
}

func TestRepositoryScopedRunnerClaimsOnlyProvenRepositoryRuns(t *testing.T) {
	for _, scope := range []string{"gh:1", "github.com/acme/repo", "github.com/acme/*", "runner-profile"} {
		for _, named := range []bool{false, true} {
			for _, proven := range []bool{false, true} {
				t.Run(scope+"/"+map[bool]string{false: "queue", true: "named"}[named]+"/"+map[bool]string{false: "asserted", true: "proven"}[proven], func(t *testing.T) {
					s, demand := unitClaimFixture(t)
					root := authorityRoot("scoped", "a")
					root.Grants = []capability.Grant{capability.NodesClaim}
					root.Repositories = []string{scope}
					if scope == "runner-profile" {
						root.Repositories = nil
						accept, err := match.ParseAccept([]string{"github.com/acme/repo"})
						if err != nil {
							t.Fatal(err)
						}
						s.claimProfiles = claimProfileFake{profile: match.Profile{Accept: accept}}
					}
					authorityPersist(t, s, root)
					if proven {
						proveClaimFixtureRepository(t, s)
					}
					demand.CapabilityPrefix = root.Prefix
					if !named {
						demand.Target = nil
						demand.Kinds = []capability.UnitKind{capability.UnitNode}
					}
					claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
					if !proven {
						if err == nil {
							t.Fatalf("repository scope accepted caller-asserted code: %+v", claim)
						}
						var denied *apierr.Error
						if named && (!errors.As(err, &denied) || denied.Code != apierr.Forbidden || !strings.Contains(denied.Message, "repository-proven")) {
							t.Fatalf("named claim hid repository proof refusal: %v", err)
						}
						return
					}
					if err != nil || claim.NodeID != "job" {
						t.Fatalf("proven repository work was refused: %+v %v", claim, err)
					}
				})
			}
		}
	}
}

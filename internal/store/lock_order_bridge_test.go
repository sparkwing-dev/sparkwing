package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// TestOnlyDelegatedClaimant persists a machine claimant delegated from the claim fixture's runner root.
func (s *Store) TestOnlyDelegatedClaimant(t *testing.T, id string) {
	t.Helper()
	c := authorityRoot(id, "a")
	c.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "runner"}
	authorityPersist(t, s, c)
}

// TestOnlyLauncher replaces the claim fixture's claimant with a funded cloud launcher and returns its prefix.
func (s *Store) TestOnlyLauncher(t *testing.T) string {
	t.Helper()
	fundUnitClaim(t, s)
	root := authorityRoot("launcher", "")
	root.Subject.Kind = capability.SubjectService
	root.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, s, root)
	s.testOnlyExec(t, `UPDATE runs SET dispatch='controller'`)
	return root.Prefix
}

// TestOnlyEvaluations makes each named job of the fixture run a finished source with a pending evaluation.
func (s *Store) TestOnlyEvaluations(t *testing.T, jobs ...string) {
	t.Helper()
	s.testOnlyExec(t, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','controller',?,3)`, s.now().Add(time.Hour).UnixNano())
	for _, job := range jobs {
		if job != "job" {
			s.testOnlyExec(t, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,?,status,ready_at,spec_json,execution_json FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`, job)
		}
	}
	for _, job := range jobs {
		s.testOnlyExec(t, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected' WHERE node_id=?`, job)
		s.testOnlyExec(t, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES ('a','run',?,7,'pending',3)`, job)
	}
}

func (s *Store) testOnlyExec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := s.exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
	projectFixtureClaimRepositories(t, s.db, s.dialect, query)
}

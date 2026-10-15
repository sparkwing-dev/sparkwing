package store_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func seedMatchNode(t *testing.T, s *store.Store, runID, repoURL string, cores float64, needs ...string) {
	t.Helper()
	ctx := context.Background()
	seedRepoTrigger(t, s, runID, repoURL, time.Now())
	plan := `{"nodes":[{"id":"work","modifiers":{"res_cores":` + strconv.FormatFloat(cores, 'f', -1, 64) + `}}]}`
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s, `UPDATE runs SET plan_json = ? WHERE id = ?`), plan, runID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: "work", Status: "pending", NeedsLabels: needs,
		RequestedCores: cores,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, runID, "work"); err != nil {
		t.Fatal(err)
	}
}

// Agent names are unique within a team, so a node selector or workload
// identity bound to a name admits one machine.
func TestRunnerTokenRefusesADuplicateAgentName(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	seedMinter(t, s, store.DefaultTeam, "owner", store.RoleOwner)
	now := time.Now().UTC()
	_, first, err := tenant.CreateMachineToken(ctx, "moonborn", []string{"nodes.claim"}, "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tenant.CreateMachineToken(ctx, "moonborn", []string{"nodes.claim"}, "owner", now); !errors.Is(err, store.ErrAgentNameTaken) {
		t.Fatalf("a second moonborn token = %v, want ErrAgentNameTaken", err)
	}
	if _, _, err := tenant.CreateMachineToken(ctx, "pi", []string{"nodes.claim"}, "owner", now); err != nil {
		t.Fatalf("another name = %v", err)
	}
	if err := tenant.RevokeTeamToken(ctx, first.Prefix, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tenant.CreateMachineToken(ctx, "moonborn", []string{"nodes.claim"}, "owner", now.Add(time.Second)); err != nil {
		t.Fatalf("re-minting moonborn after revoking it = %v", err)
	}
}

// Every mint path refuses a second live token for an agent name; rotation is
// the one overlap, with the predecessor it revokes at the end of its grace.
func TestAgentNameIsUniqueOnEveryMintPath(t *testing.T) {
	s := storetest.Open(t)
	now := time.Now().UTC()
	_, first, err := s.CreateToken("agent:moonborn", store.TokenKindRunner, []string{"nodes.claim"}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateToken("agent:moonborn", store.TokenKindRunner, []string{"nodes.claim"}, 0, now); !errors.Is(err, store.ErrAgentNameTaken) {
		t.Fatalf("a second moonborn token through CreateToken = %v, want ErrAgentNameTaken", err)
	}
	if _, _, err := s.CreateToken("agent:moonborn", store.TokenKindUser, []string{"runs.read"}, 0, now); err != nil {
		t.Fatalf("a user token that shares the principal = %v", err)
	}
	if _, _, _, err := s.RotateToken(first.Prefix, time.Hour, 0, now); err != nil {
		t.Fatalf("rotating moonborn = %v", err)
	}
	if _, _, err := s.CreateToken("agent:moonborn", store.TokenKindRunner, []string{"nodes.claim"}, 0, now); !errors.Is(err, store.ErrAgentNameTaken) {
		t.Fatalf("a mint during the rotation grace = %v, want ErrAgentNameTaken", err)
	}
}

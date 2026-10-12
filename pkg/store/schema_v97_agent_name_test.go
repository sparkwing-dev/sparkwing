package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func seedV96AgentNameIndex(t *testing.T, rows ...[2]string) *storetest.Target {
	t.Helper()
	target := storetest.New(t)
	seeded, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seeded.Close() }()
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_tokens_agent_name`,
		`CREATE UNIQUE INDEX idx_tokens_agent_name ON tokens(team, principal)
			WHERE kind = 'runner' AND revoked_at IS NULL AND substr(principal, 1, 6) = 'agent:'`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 97`,
	} {
		if _, err := seeded.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for i, row := range rows {
		if err := insertAgentRow(t, seeded, "sw_seed0000"+string(rune('a'+i)), row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

func insertAgentRow(t *testing.T, s *store.Store, prefix, principal, kind string) error {
	t.Helper()
	_, err := s.DB().Exec(storetest.Rebind(s,
		`INSERT INTO tokens (team, hash, prefix, principal, kind, scopes, created_at) VALUES (?, ?, ?, ?, ?, 'nodes.claim', ?)`),
		string(store.DefaultTeam), "hash-"+prefix, prefix, principal, kind, time.Now().Unix())
	return err
}

// v97 replaces the runner-only agent-name index, which CREATE ... IF NOT
// EXISTS would keep, with one that also holds machine team tokens.
func TestSchemaV97_AgentNameIndexHoldsMachineTeamTokens(t *testing.T) {
	upgraded, err := seedV96AgentNameIndex(t).TryOpen()
	if err != nil {
		t.Fatalf("upgrade = %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	if v := readSchemaVersion(t, upgraded.DB()); v != store.ExpectedSchemaVersion() {
		t.Fatalf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	if err := insertAgentRow(t, upgraded, "swr_box00000", "agent:box", store.TokenKindRunner); err != nil {
		t.Fatal(err)
	}
	if err := insertAgentRow(t, upgraded, "swt_box00000", "agent:box", store.TokenKindTeam); err == nil {
		t.Fatal("the upgraded index admitted a machine team token beside a live runner token of the same name")
	}
	if err := insertAgentRow(t, upgraded, "swt_ed000000", "ed@example.com", store.TokenKindTeam); err != nil {
		t.Fatalf("a member team token = %v, want it outside the index", err)
	}
}

func TestSchemaV97_RefusesDuplicateNamesAcrossKinds(t *testing.T) {
	target := seedV96AgentNameIndex(t, [2]string{"agent:twin", store.TokenKindRunner}, [2]string{"agent:twin", store.TokenKindTeam})
	if _, err := target.TryOpen(); err == nil || !strings.Contains(err.Error(), "agent:twin") {
		t.Fatalf("upgrade over a runner and a machine team token both named agent:twin = %v, want a refusal naming it", err)
	}
}

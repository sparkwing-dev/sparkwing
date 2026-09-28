package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// Racing mints of one agent name pass the read that refuses a taken name
// together, so only the database can leave one of them standing. Half take
// the team-settings path that locks the team row and half the admin path
// that does not.
func TestAgentNameMintRaceLeavesOneLiveTokenSQLiteAndPostgres(t *testing.T) {
	for name, open := range map[string]func(*testing.T) *store.Store{
		"sqlite":   storetest.OpenSQLite,
		"postgres": storetest.OpenPostgres,
	} {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			ctx := context.Background()
			tenant, err := s.ForTeam(ctx, store.DefaultTeam)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			const minters = 8
			start := make(chan struct{})
			errs := make([]error, minters)
			var wg sync.WaitGroup
			for i := range minters {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if i%2 == 0 {
						_, _, errs[i] = s.CreateToken("agent:race", store.TokenKindRunner, []string{"nodes.claim"}, 0, now)
						return
					}
					_, _, errs[i] = tenant.CreateRunnerToken(ctx, "agent:race", []string{"nodes.claim"}, "owner", now)
				}()
			}
			close(start)
			wg.Wait()
			won := 0
			for i, err := range errs {
				switch {
				case err == nil:
					won++
				case !errors.Is(err, store.ErrAgentNameTaken):
					t.Errorf("minter %d = %v, want success or ErrAgentNameTaken", i, err)
				}
			}
			if won != 1 {
				t.Fatalf("%d of %d racing mints succeeded, want exactly 1", won, minters)
			}
			if live := liveAgentTokens(t, s, "agent:race", now); live != 1 {
				t.Fatalf("live agent:race tokens = %d, want 1", live)
			}
		})
	}
}

// The index holds unrevoked rows and cannot read the clock, so an expired
// token must stop holding its name, and a rotation must still overlap its
// predecessor until the grace ends.
func TestAgentNameIndexKeepsExpiryAndRotationWorking(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := tenant.CreateToken(ctx, "agent:brief", store.TokenKindRunner, []string{"nodes.claim"}, time.Minute, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Minute)
	raw, tok, err := tenant.CreateToken(ctx, "agent:brief", store.TokenKindRunner, []string{"nodes.claim"}, 0, later)
	if err != nil {
		t.Fatalf("re-minting a name whose token expired = %v", err)
	}
	newRaw, _, oldTok, err := s.RotateToken(tok.Prefix, time.Hour, 0, later)
	if err != nil {
		t.Fatalf("rotating agent:brief = %v", err)
	}
	for label, r := range map[string]string{"predecessor": raw, "successor": newRaw} {
		if _, err := s.LookupToken(r, later.Add(time.Minute)); err != nil {
			t.Errorf("%s inside the grace = %v, want it to authenticate", label, err)
		}
	}
	if oldTok.ReplacedBy == "" {
		t.Error("the predecessor names no successor")
	}
	if _, err := s.LookupToken(raw, later.Add(2*time.Hour)); err == nil {
		t.Error("the predecessor authenticates after its grace")
	}
}

// Duplicates already in a store make v82 refuse the upgrade and name them,
// while an expired duplicate is retired rather than counted.
func TestSchemaV82_RefusesExistingDuplicateAgentNames(t *testing.T) {
	now := time.Now().UTC()
	twin := seedV81Duplicate(t, "agent:twin", nil, now)
	if _, err := twin.TryOpen(); err == nil || !strings.Contains(err.Error(), "agent:twin") {
		t.Fatalf("upgrade over two live agent:twin tokens = %v, want a refusal naming agent:twin", err)
	}

	stale := seedV81Duplicate(t, "agent:stale", now.Add(-time.Hour).Unix(), now)
	upgraded, err := stale.TryOpen()
	if err != nil {
		t.Fatalf("upgrade over an expired duplicate = %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	if v := readSchemaVersion(t, upgraded.DB()); v != store.ExpectedSchemaVersion() {
		t.Fatalf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	if _, _, err := upgraded.CreateToken("agent:stale", store.TokenKindRunner, []string{"nodes.claim"}, 0, now); !errors.Is(err, store.ErrAgentNameTaken) {
		t.Fatalf("a mint over the surviving agent:stale token = %v, want ErrAgentNameTaken", err)
	}
}

func seedV81Duplicate(t *testing.T, principal string, expires any, now time.Time) *storetest.Target {
	t.Helper()
	target := storetest.New(t)
	seeded, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seeded.Close() }()
	if _, _, err := seeded.CreateToken(principal, store.TokenKindRunner, []string{"nodes.claim"}, 0, now); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_tokens_agent_name`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 82`,
	} {
		if _, err := seeded.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := seeded.DB().Exec(storetest.Rebind(seeded,
		`INSERT INTO tokens (team, hash, prefix, principal, kind, scopes, created_at, expires_at)
		 VALUES (?, 'hash-dup', 'swr_dup00', ?, 'runner', 'nodes.claim', ?, ?)`),
		string(store.DefaultTeam), principal, now.Unix(), expires); err != nil {
		t.Fatalf("seed duplicate: %v", err)
	}
	return target
}

func liveAgentTokens(t *testing.T, s *store.Store, principal string, now time.Time) int {
	t.Helper()
	toks, err := s.ListTokens(store.TokenKindRunner, true)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tok := range toks {
		if tok.Principal == principal && tok.IsValid(now) {
			n++
		}
	}
	return n
}

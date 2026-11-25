package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// StatementCount is what the store sent to SQLite between two reads of a counter.
type StatementCount struct {
	Statements, ReadTransactions, WriteTransactions, Writes, TeamLocks int
}

// StatementCounter counts the statements and transactions a store sends to SQLite.
type StatementCounter struct {
	mu    sync.Mutex
	count StatementCount
}

// Take returns the count since the last Take and starts a new one.
func (c *StatementCounter) Take() StatementCount {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.count
	c.count = StatementCount{}
	return out
}

func (c *StatementCounter) statement(query string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count.Statements++
	verb, _, _ := strings.Cut(strings.TrimSpace(query), " ")
	switch strings.ToUpper(verb) {
	case "INSERT", "UPDATE", "DELETE":
		c.count.Writes++
	}
	if strings.HasPrefix(query, "SELECT name FROM teams WHERE name=?") {
		c.count.TeamLocks++
	}
}

func (c *StatementCounter) begin(read bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if read {
		c.count.ReadTransactions++
	} else {
		c.count.WriteTransactions++
	}
}

// TestOnlyCountStatements reopens a SQLite store's database through a counting
// connection; the store sends every later statement through it.
func (s *Store) TestOnlyCountStatements(t *testing.T) *StatementCounter {
	t.Helper()
	if s.dialect != DialectSQLite {
		t.Skip("statement counts read the SQLite driver")
	}
	var path string
	if err := s.db.QueryRowContext(t.Context(), `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	counter := &StatementCounter{}
	db := sql.OpenDB(countingConnector{dsn: sqliteDSN(path), counter: counter})
	db.SetMaxOpenConns(1)
	previous := s.db
	s.db = db
	t.Cleanup(func() {
		s.db = previous
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	counter.Take()
	return counter
}

type countingConnector struct {
	dsn     string
	counter *StatementCounter
}

func (c countingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := statementCachingDriver{}.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &countingConn{statementCachingConn: conn.(*statementCachingConn), counter: c.counter}, nil
}

func (countingConnector) Driver() driver.Driver { return statementCachingDriver{} }

type countingConn struct {
	*statementCachingConn
	counter *StatementCounter
}

func (c *countingConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.counter.statement(query)
	return c.statementCachingConn.PrepareContext(ctx, query)
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.counter.begin(opts.ReadOnly)
	return c.statementCachingConn.BeginTx(ctx, opts)
}

// ClaimableUnitQueuedPlan is PostgreSQL's plan for the empty-poll existence check over every unit kind,
// planned with sequential scans disabled so that only an index the predicates match avoids one.
func ClaimableUnitQueuedPlan(t *testing.T, s *Store, team string) string {
	t.Helper()
	if s.dialect != DialectPostgres {
		t.Skip("the plan guard reads the PostgreSQL planner")
	}
	query, args := claimableUnitQueuedSQL(team, []capability.UnitKind{capability.UnitNode, capability.UnitEval, capability.UnitTrigger, capability.UnitPlan})
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(t.Context(), `EXPLAIN `+rewritePh(DialectPostgres, query), args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(plan, "\n")
}

// TestOnlyStampAfterAward reads an unchanged poll, lets an award consume one of the claimant's slots, then
// restamps that poll, and reports whether the restamp applied and the free slots the row holds afterwards.
func (s *Store) TestOnlyStampAfterAward(ctx context.Context, t *testing.T, observation UnitDemand) (bool, int) {
	t.Helper()
	o := s.AsOperator()
	observation.Labels = match.SelfAsserted(observation.Labels)
	labels, err := json.Marshal(observation.Labels)
	if err != nil {
		t.Fatal(err)
	}
	team, seen, unchanged, err := o.runnerPollUnchanged(ctx, observation, labels)
	if err != nil || !unchanged {
		t.Fatalf("the poll was not read as unchanged: %v", err)
	}
	if err := s.inTx(ctx, func(tx *storeTx) error { return consumeClaimSlotTx(ctx, tx, observation.CapabilityPrefix, team) }); err != nil {
		t.Fatal(err)
	}
	stamped, err := o.stampRunnerPoll(ctx, observation, team, seen)
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	if err := s.db.QueryRowContext(ctx, rewritePh(s.dialect, `SELECT claim_observation_json FROM capabilities WHERE prefix=?`), observation.CapabilityPrefix).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var stored runnerPoll
	if err := json.Unmarshal(body, &stored); err != nil || stored.FreeSlots == nil {
		t.Fatalf("stored observation %s: %v", body, err)
	}
	return stamped, *stored.FreeSlots
}

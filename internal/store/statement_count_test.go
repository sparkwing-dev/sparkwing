package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// StatementCount is what the store sent to SQLite between two reads of a counter. StandingReads counts the
// teams whose Cloud standing was read, and PaymentModeReads the reads of the payment mode setting.
type StatementCount struct {
	Statements, ReadTransactions, WriteTransactions, Writes, TeamLocks, StandingReads, PaymentModeReads int
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
	if strings.HasPrefix(query, "SELECT billing_trust,billing_trust_by,") {
		c.count.StandingReads++
	}
	if strings.Contains(query, "FROM settings WHERE key='payment_mode'") {
		c.count.PaymentModeReads++
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
	query, args := claimableUnitQueuedSQL(team, []capability.UnitKind{capability.UnitNode, capability.UnitEval, capability.UnitTrigger, capability.UnitPlan})
	return explainWithoutSeqScan(t, s, query, args)
}

// LauncherClaimTeamsPlan is PostgreSQL's plan for the Cloud launcher's existence check across live teams over every
// unit kind, planned with sequential scans disabled.
func LauncherClaimTeamsPlan(t *testing.T, s *Store) string {
	t.Helper()
	q := claimSQL{}
	queued := q.queuedUnitSQL(func() string { return "tm.name" }, []capability.UnitKind{capability.UnitNode, capability.UnitEval, capability.UnitPlan})
	return explainWithoutSeqScan(t, s, `SELECT tm.name FROM teams tm WHERE `+queued, q.args)
}

func explainWithoutSeqScan(t *testing.T, s *Store, query string, args []any) string {
	t.Helper()
	if s.dialect != DialectPostgres {
		t.Skip("the plan guard reads the PostgreSQL planner")
	}
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

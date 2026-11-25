package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync"
	"testing"
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

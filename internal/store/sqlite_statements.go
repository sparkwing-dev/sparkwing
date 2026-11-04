package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	modernsqlite "modernc.org/sqlite"
)

// perf: SQLite compiled every statement on every call, and parsing the store's long statements took about
// 40% of the local controller's CPU during a run; this driver keeps each connection's prepared statements.
const cachedSQLiteDriver = "sparkwing-sqlite"

// safety: dynamically built statements must not grow the cache without bound; past this they run uncached.
const maxCachedStatements = 512

func init() {
	sql.Register(cachedSQLiteDriver, statementCachingDriver{})
}

type statementCachingDriver struct{}

func (statementCachingDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&modernsqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &statementCachingConn{Conn: conn, statements: map[string]*cachedStatement{}}, nil
}

// hack: omitting ExecerContext and QueryerContext makes database/sql route every call through PrepareContext.
type statementCachingConn struct {
	driver.Conn
	statements map[string]*cachedStatement
}

type cachedStatement struct {
	driver.Stmt
	busy bool
}

func (c *statementCachingConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *statementCachingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if cached, ok := c.statements[query]; ok && !cached.busy {
		cached.busy = true
		return reusedStatement{cached}, nil
	}
	stmt, err := c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	if _, ok := c.statements[query]; ok || len(c.statements) >= maxCachedStatements {
		return stmt, nil
	}
	cached := &cachedStatement{Stmt: stmt, busy: true}
	c.statements[query] = cached
	return reusedStatement{cached}, nil
}

func (c *statementCachingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *statementCachingConn) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *statementCachingConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *statementCachingConn) IsValid() bool {
	return c.Conn.(driver.Validator).IsValid()
}

func (c *statementCachingConn) Close() error {
	var err error
	for query, cached := range c.statements {
		err = errors.Join(err, cached.Stmt.Close())
		delete(c.statements, query)
	}
	return errors.Join(err, c.Conn.Close())
}

// safety: database/sql closes a statement after its call or its rows, which returns it to the cache; a busy
// statement is never shared, so an open cursor keeps its own.
type reusedStatement struct{ cached *cachedStatement }

func (s reusedStatement) Close() error {
	s.cached.busy = false
	return nil
}

func (s reusedStatement) NumInput() int { return s.cached.NumInput() }

func (s reusedStatement) Exec(args []driver.Value) (driver.Result, error) {
	return s.cached.Exec(args) //nolint:staticcheck // database/sql calls ExecContext; this satisfies driver.Stmt
}

func (s reusedStatement) Query(args []driver.Value) (driver.Rows, error) {
	return s.cached.Query(args) //nolint:staticcheck // database/sql calls QueryContext; this satisfies driver.Stmt
}

func (s reusedStatement) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.cached.Stmt.(driver.StmtExecContext).ExecContext(ctx, args)
}

func (s reusedStatement) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.cached.Stmt.(driver.StmtQueryContext).QueryContext(ctx, args)
}

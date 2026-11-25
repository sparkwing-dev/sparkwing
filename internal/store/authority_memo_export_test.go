package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

var queryHookDriverSequence atomic.Uint64

// OpenQueryHookPostgresForTest opens a second store on dsn whose every
// statement first passes through beforeQuery on the issuing goroutine.
func OpenQueryHookPostgresForTest(t testing.TB, dsn string, beforeQuery func(string)) *Store {
	t.Helper()
	cfg, err := postgresConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := stdlib.RegisterConnConfig(cfg)
	t.Cleanup(func() { stdlib.UnregisterConnConfig(name) })
	driverName := fmt.Sprintf("sparkwing_query_hook_pgx_%d", queryHookDriverSequence.Add(1))
	sql.Register(driverName, queryHookDriver{base: stdlib.GetDefaultDriver(), hook: beforeQuery})
	st, err := openSQL(t.Context(), driverName, name, DialectPostgres, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ResolveLockedAuthorityForTest resolves ref with its ancestry locks in one write transaction.
func (s *Store) ResolveLockedAuthorityForTest(ctx context.Context, ref capability.AuthorityRef) error {
	return s.inTx(ctx, func(tx *storeTx) error {
		_, err := tx.ResolveAuthority(ctx, ref)
		return err
	})
}

// HoldResolvedAuthorityForTest resolves ref with its ancestry locks, then
// calls held with the transaction's backend pid before committing.
func (s *Store) HoldResolvedAuthorityForTest(ctx context.Context, ref capability.AuthorityRef, held func(pid int)) error {
	return s.inTxOnce(ctx, func(tx *storeTx) error {
		if _, err := tx.ResolveAuthority(ctx, ref); err != nil {
			return err
		}
		var pid int
		if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		held(pid)
		return nil
	})
}

type queryHookDriver struct {
	base driver.Driver
	hook func(string)
}

func (d queryHookDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &queryHookConn{Conn: conn, hook: d.hook}, nil
}

// hack: forwarding pgx's optional interfaces keeps the store's driver behavior identical apart from the hook.
type queryHookConn struct {
	driver.Conn
	hook func(string)
}

func (c *queryHookConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.hook(query)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *queryHookConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.hook(query)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *queryHookConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *queryHookConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (c *queryHookConn) CheckNamedValue(value *driver.NamedValue) error {
	return c.Conn.(driver.NamedValueChecker).CheckNamedValue(value)
}

func (c *queryHookConn) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *queryHookConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestTransactionClockSnapshot(t *testing.T) {
	s := authorityStore(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	calls := 0
	s.clock = func() time.Time { calls++; return now }
	tx, err := s.beginTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	want := now
	now = now.Add(time.Hour)
	if !tx.time.Equal(want) || calls != 1 {
		t.Fatalf("snapshot = %v, clock calls = %d; want %v, 1", tx.time, calls, want)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	next, err := s.beginSnapshotReadTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(next)
	if !next.time.Equal(now) || calls != 2 {
		t.Fatalf("next snapshot = %v, clock calls = %d; want %v, 2", next.time, calls, now)
	}
}

func TestTransactionClockFailureRefusesReadAndWrite(t *testing.T) {
	for _, dialect := range []Dialect{DialectSQLite, DialectPostgres} {
		for _, read := range []bool{false, true} {
			t.Run(dialect.String()+map[bool]string{false: "/write", true: "/read"}[read], func(t *testing.T) {
				clockErr := errors.New("database clock unavailable")
				connector := &clockFailureConnector{err: clockErr}
				db := sql.OpenDB(connector)
				defer db.Close()
				s := &Store{db: db, dialect: dialect}
				var tx *storeTx
				var err error
				if read {
					tx, err = s.beginSnapshotReadTx(t.Context())
				} else {
					tx, err = s.beginTx(t.Context())
				}
				if tx != nil {
					_ = tx.Rollback()
					t.Fatal("transaction escaped without a clock snapshot")
				}
				if !errors.Is(err, clockErr) || connector.rollbacks != 1 {
					t.Fatalf("error = %v, rollbacks = %d", err, connector.rollbacks)
				}
			})
		}
	}
}

type clockFailureConnector struct {
	err       error
	rollbacks int
}

func (c *clockFailureConnector) Connect(context.Context) (driver.Conn, error) {
	return clockFailureConn{c}, nil
}
func (c *clockFailureConnector) Driver() driver.Driver { return clockFailureDriver{} }

type clockFailureDriver struct{}

func (clockFailureDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type clockFailureConn struct{ c *clockFailureConnector }

func (clockFailureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (clockFailureConn) Close() error                { return nil }
func (c clockFailureConn) Begin() (driver.Tx, error) { return clockFailureTx{c.c}, nil }
func (c clockFailureConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

func (c clockFailureConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, c.c.err
}

type clockFailureTx struct{ c *clockFailureConnector }

func (clockFailureTx) Commit() error      { return nil }
func (tx clockFailureTx) Rollback() error { tx.c.rollbacks++; return nil }

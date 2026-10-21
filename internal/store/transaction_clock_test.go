package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
	"time"
)

func transactionTime(t *testing.T, tx *storeTx) time.Time {
	t.Helper()
	now, err := tx.now(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

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
	now = now.Add(time.Hour)
	if current, err := tx.now(t.Context()); err != nil || !current.Equal(now) || calls != 2 {
		t.Fatalf("write fence clock = %v, calls = %d, error = %v", current, calls, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	next, err := s.beginSnapshotReadTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(next)
	want := now
	now = now.Add(time.Hour)
	for range 2 {
		if current, err := next.now(t.Context()); err != nil || !current.Equal(want) || calls != 3 {
			t.Fatalf("read snapshot clock = %v, calls = %d, error = %v", current, calls, err)
		}
	}
}

func TestTransactionFenceClockFailure(t *testing.T) {
	for _, dialect := range []Dialect{DialectSQLite, DialectPostgres} {
		t.Run(dialect.String(), func(t *testing.T) {
			connector := &clockFailureConnector{}
			db := sql.OpenDB(connector)
			defer db.Close()
			s := &Store{db: db, dialect: dialect}
			tx, err := s.beginTx(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackOrLog(tx)
			connector.err = errors.New("database clock lost during policy work")
			if _, err := tx.now(t.Context()); !errors.Is(err, connector.err) {
				t.Fatalf("fence clock failure = %v", err)
			}
		})
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
	if c.c.err == nil {
		return &clockValueRows{}, nil
	}
	return nil, c.c.err
}

type clockValueRows struct{ read bool }

func (*clockValueRows) Columns() []string { return []string{"now"} }
func (*clockValueRows) Close() error      { return nil }
func (r *clockValueRows) Next(values []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read, values[0] = true, int64(1)
	return nil
}

type clockFailureTx struct{ c *clockFailureConnector }

func (clockFailureTx) Commit() error      { return nil }
func (tx clockFailureTx) Rollback() error { tx.c.rollbacks++; return nil }

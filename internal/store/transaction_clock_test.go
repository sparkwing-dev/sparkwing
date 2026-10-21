package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func transactionTime(t *testing.T, tx *storeTx) time.Time {
	t.Helper()
	now := tx.now(t.Context())
	if tx.err != nil {
		t.Fatal(tx.err)
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
	if current := tx.now(t.Context()); !current.Equal(now) || calls != 2 {
		t.Fatalf("write fence clock = %v, calls = %d", current, calls)
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
		if current := next.now(t.Context()); !current.Equal(want) || calls != 3 {
			t.Fatalf("read snapshot clock = %v, calls = %d", current, calls)
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
			clockErr := errors.New("database clock lost during policy work")
			connector.err = clockErr
			tx.now(t.Context())
			connector.err = nil
			queries := connector.queries
			for name, call := range map[string]func() error{
				"exec":          func() error { _, err := tx.Exec(`SELECT 1`); return err },
				"exec context":  func() error { _, err := tx.ExecContext(t.Context(), `SELECT 1`); return err },
				"query":         func() error { _, err := tx.Query(`SELECT 1`); return err },
				"query context": func() error { _, err := tx.QueryContext(t.Context(), `SELECT 1`); return err },
				"row":           func() error { var n int; return tx.QueryRow(`SELECT 1`).Scan(&n) },
				"row context":   func() error { var n int; return tx.QueryRowContext(t.Context(), `SELECT 1`).Scan(&n) },
				"ledger": func() error {
					_, err := (&ledgerTx{storeTx: tx}).exec(t.Context(), `INSERT INTO credit_grants VALUES (1)`)
					return err
				},
				"commit": tx.Commit,
			} {
				if err := call(); !errors.Is(err, clockErr) {
					t.Errorf("%s after clock failure = %v", name, err)
				}
			}
			if !tx.now(t.Context()).IsZero() || connector.queries != queries || connector.writes != 0 || connector.commits != 0 || connector.rollbacks != 1 {
				t.Fatalf("poisoned transaction reached database: %+v", connector)
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

func TestTransactionClockFailureRollsBackWrites(t *testing.T) {
	for _, bodyError := range []error{nil, errors.New("unrelated refusal")} {
		t.Run(fmt.Sprint(bodyError), func(t *testing.T) {
			s := authorityStore(t)
			connection := recordStoreFences(t, s)
			clockErr := errors.New("clock lost after first write")
			secondWrites := 0
			connection.beforeExec = func(query string) {
				if strings.Contains(query, "after clock failure") {
					secondWrites++
				}
			}
			err := s.inTx(t.Context(), func(tx *storeTx) error {
				if _, err := tx.ExecContext(t.Context(), `INSERT INTO teams(name,created_at,updated_at) VALUES ('before clock failure',1,1)`); err != nil {
					return err
				}
				s.pinnedClock = nil
				connection.beforeQuery = func(query string) error {
					if query == databaseTimeSQL(s.dialect) {
						return clockErr
					}
					return nil
				}
				tx.now(t.Context())
				// safety: a recovered clock cannot unpoison this transaction.
				connection.beforeQuery = nil
				if _, err := tx.ExecContext(t.Context(), `INSERT INTO teams(name,created_at,updated_at) VALUES ('after clock failure',1,1)`); !errors.Is(err, clockErr) {
					t.Errorf("write after clock failure = %v", err)
				}
				return bodyError
			})
			if !errors.Is(err, clockErr) || secondWrites != 0 {
				t.Fatalf("transaction error = %v, later writes = %d", err, secondWrites)
			}
			var writes int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM teams WHERE name IN ('before clock failure','after clock failure')`).Scan(&writes); err != nil || writes != 0 {
				t.Fatalf("committed writes = %d, error = %v", writes, err)
			}
		})
	}
}

type clockFailureConnector struct {
	err       error
	rollbacks int
	queries   int
	writes    int
	commits   int
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
	c.c.queries++
	if c.c.err == nil {
		return &clockValueRows{}, nil
	}
	return nil, c.c.err
}

func (c clockFailureConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.c.writes++
	return driver.RowsAffected(1), nil
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

func (tx clockFailureTx) Commit() error   { tx.c.commits++; return nil }
func (tx clockFailureTx) Rollback() error { tx.c.rollbacks++; return nil }

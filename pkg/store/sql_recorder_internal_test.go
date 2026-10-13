package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	modernsqlite "modernc.org/sqlite"
)

var recordingSQLiteDriverSequence atomic.Uint64

func newRecordingStore(t *testing.T) (*Store, *sqlStatementRecorder) {
	t.Helper()
	recorder := &sqlStatementRecorder{}
	driverName := fmt.Sprintf("sparkwing_recording_sqlite_%d", recordingSQLiteDriverSequence.Add(1))
	sql.Register(driverName, &recordingSQLiteDriver{recorder: recorder})
	path := filepath.Join(t.TempDir(), "state.db")
	if err := preparePrivateSQLite(path); err != nil {
		t.Fatal(err)
	}
	st, err := openSQL(context.Background(), driverName, sqliteDSN(path), DialectSQLite, "delete "+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, recorder
}

type sqlStatementRecorder struct {
	mu         sync.Mutex
	statements []string
}

func (r *sqlStatementRecorder) record(statement string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, statement)
}

func (r *sqlStatementRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = nil
}

func (r *sqlStatementRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.statements...)
}

type recordingSQLiteDriver struct{ recorder *sqlStatementRecorder }

func (d *recordingSQLiteDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&modernsqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &recordingSQLiteConn{Conn: conn, recorder: d.recorder}, nil
}

type recordingSQLiteConn struct {
	driver.Conn
	recorder *sqlStatementRecorder
}

func (c *recordingSQLiteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.recorder.record(query)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *recordingSQLiteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.recorder.record(query)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *recordingSQLiteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *recordingSQLiteConn) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *recordingSQLiteConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *recordingSQLiteConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

func (c *recordingSQLiteConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

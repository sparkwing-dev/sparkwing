package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modernsqlite "modernc.org/sqlite"
)

func TestExecutorClaimOfferQueryCountDoesNotGrowWithFleet(t *testing.T) {
	one := pendingExecutorOfferStatements(t, 1)
	full := pendingExecutorOfferStatements(t, MaxEnrolledExecutors)
	if len(full) != len(one) {
		t.Fatalf("offer statements at fleet limit = %d, want one-executor count %d", len(full), len(one))
	}
	for _, statement := range full {
		normalized := strings.Join(strings.Fields(strings.ToLower(statement)), " ")
		if strings.Contains(normalized, "from executors order by kind, name") ||
			strings.Contains(normalized, "select claim_executor, claim_slot, claim_reservation") {
			t.Fatalf("ordinary pending offer scanned fleet state: %s", normalized)
		}
	}
}

func pendingExecutorOfferStatements(t *testing.T, fleetSize int) []string {
	t.Helper()
	st, recorder := newRecordingExecutorStore(t)
	seedExecutorRegistrations(t, st, fleetSize)
	ctx := context.Background()
	if err := st.CreateRun(ctx, Run{ID: "run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, Node{RunID: "run", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET offer_priority_target = 100 WHERE run_id = 'run' AND node_id = 'work'`); err != nil {
		t.Fatal(err)
	}
	summary, err := st.SchedulingSummary(ctx, "run", "work")
	if err != nil {
		t.Fatal(err)
	}
	recorder.reset()
	result, err := st.TestOnlyOfferExecutorClaim(ctx, ClaimIdentity{Principal: "principal-seed-0", TokenPrefix: "swr_seed-0"}, ExecutorClaimOffer{
		ExecutorName: "seed-0", HolderID: "holder", RunID: "run", NodeID: "work",
		ReservationID: "reservation", ResourceDigest: summary.ResourceDigest, Slot: 0, Lease: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pending || result.Node != nil {
		t.Fatalf("offer result = %+v, want pending", result)
	}
	return recorder.snapshot()
}

var recordingSQLiteDriverSequence atomic.Uint64

func newRecordingExecutorStore(t *testing.T) (*Store, *sqlStatementRecorder) {
	t.Helper()
	recorder := &sqlStatementRecorder{}
	driverName := fmt.Sprintf("sparkwing_recording_sqlite_%d", recordingSQLiteDriverSequence.Add(1))
	sql.Register(driverName, &recordingSQLiteDriver{recorder: recorder})
	path := filepath.Join(t.TempDir(), "state.db")
	if err := preparePrivateSQLite(path); err != nil {
		t.Fatal(err)
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := openSQL(driverName, dsn, DialectSQLite)
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

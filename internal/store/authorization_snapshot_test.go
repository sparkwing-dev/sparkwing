package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"

	modernsqlite "modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type authorizationConnector struct{ conn *authorizationConnection }

func (c authorizationConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c authorizationConnector) Driver() driver.Driver                        { return &modernsqlite.Driver{} }

type authorizationLock struct {
	query string
	args  []driver.NamedValue
}

type authorizationConnection struct {
	driver.Conn
	options     driver.TxOptions
	locks       []authorizationLock
	rowsOpen    bool
	beforeLock  func(string)
	beforeQuery func(string) error
	beforeExec  func(string)
}

func (c *authorizationConnection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.options = options
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, driver.TxOptions{})
}

func (c *authorizationConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.rowsOpen {
		return nil, errors.New("conn busy: query before closing rows")
	}
	if c.beforeQuery != nil {
		if err := c.beforeQuery(query); err != nil {
			return nil, err
		}
	}
	for _, suffix := range []string{" FOR NO KEY UPDATE", " FOR UPDATE", " FOR SHARE", " FOR KEY SHARE"} {
		if strings.HasSuffix(query, suffix) {
			if c.options.ReadOnly {
				return nil, errors.New("row lock in read-only authorization transaction")
			}
			c.locks = append(c.locks, authorizationLock{query: query, args: append([]driver.NamedValue(nil), args...)})
			if c.beforeLock != nil {
				c.beforeLock(query)
			}
			query = strings.TrimSuffix(query, suffix)
		}
	}
	if query == databaseTimeSQL(DialectPostgres) {
		query = databaseTimeSQL(DialectSQLite)
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	c.rowsOpen = true
	return &authorizationRows{Rows: rows, conn: c}, nil
}

type authorizationRows struct {
	driver.Rows
	conn *authorizationConnection
}

func (r *authorizationRows) Close() error {
	err := r.Rows.Close()
	r.conn.rowsOpen = false
	return err
}

func (c *authorizationConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.beforeExec != nil {
		c.beforeExec(query)
	}
	if c.options.ReadOnly {
		return nil, errors.New("write in read-only authorization transaction")
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func singleCursorStore(t *testing.T) (*Store, *authorizationConnection) {
	t.Helper()
	conn, err := (&modernsqlite.Driver{}).Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	recording := &authorizationConnection{Conn: conn}
	db := sql.OpenDB(authorizationConnector{conn: recording})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	rewriteSchemaExec(t, db, schemaNeutral)
	return &Store{db: db, dialect: DialectSQLite}, recording
}

func TestAuthorizationSnapshotReadsWithoutLocksAndMutationLocksAncestry(t *testing.T) {
	s, recording := singleCursorStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	root := authorityRoot("root", "a")
	authorityPersist(t, s, root)
	child := authorityRoot("child", "a")
	child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	authorityPersist(t, s, child)
	s.dialect = DialectPostgres
	p := capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}}
	if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindNone, capability.Target{Team: "a"}); err != nil {
		t.Fatal(err)
	}
	if !recording.options.ReadOnly || recording.options.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) {
		t.Fatalf("authorization snapshot options=%+v", recording.options)
	}
	if len(recording.locks) != 0 {
		t.Fatalf("authorization snapshot acquired locks: %+v", recording.locks)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return tx.CheckBinding(t.Context(), p, capability.BindNone, capability.Target{Team: "a"})
	}); err != nil {
		t.Fatal(err)
	}
	want := []authorizationLock{
		{query: "SELECT name FROM teams WHERE name=$1 FOR SHARE", args: []driver.NamedValue{{Ordinal: 1, Value: "a"}}},
		{query: "SELECT prefix FROM capabilities WHERE prefix=$1 FOR NO KEY UPDATE", args: []driver.NamedValue{{Ordinal: 1, Value: child.Prefix}}},
		{query: "SELECT prefix FROM capabilities WHERE prefix=$1 FOR NO KEY UPDATE", args: []driver.NamedValue{{Ordinal: 1, Value: root.Prefix}}},
	}
	if recording.options.ReadOnly || !reflect.DeepEqual(recording.locks, want) {
		t.Fatalf("mutation locks: options=%+v got=%+v want=%+v", recording.options, recording.locks, want)
	}
}

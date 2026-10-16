package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	modernsqlite "modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type authorizationConnector struct{ conn *authorizationConnection }

func (c authorizationConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c authorizationConnector) Driver() driver.Driver                        { return &modernsqlite.Driver{} }

type authorizationConnection struct {
	driver.Conn
	options driver.TxOptions
	locks   int
}

func (c *authorizationConnection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.options = options
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, driver.TxOptions{})
}

func (c *authorizationConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	for _, suffix := range []string{" FOR NO KEY UPDATE", " FOR UPDATE", " FOR SHARE", " FOR KEY SHARE"} {
		if strings.HasSuffix(query, suffix) {
			if c.options.ReadOnly {
				return nil, errors.New("row lock in read-only authorization transaction")
			}
			c.locks++
			query = strings.TrimSuffix(query, suffix)
		}
	}
	if query == databaseTimeSQL(DialectPostgres) {
		query = databaseTimeSQL(DialectSQLite)
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *authorizationConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.options.ReadOnly {
		return nil, errors.New("write in read-only authorization transaction")
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func TestAuthorizationSnapshotReadsWithoutLocksAndMutationLocksAncestry(t *testing.T) {
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
	rewriteSchemaExec(t, db, "INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)")
	s := &Store{db: db, dialect: DialectSQLite}
	root := authorityRoot("root", "a")
	authorityPersist(t, s, root)
	child := authorityRoot("child", "a")
	child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	authorityPersist(t, s, child)
	s.dialect = DialectPostgres
	p := capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}}
	if err := s.CheckBinding(t.Context(), p, capability.BindNone, capability.Target{Team: "a"}); err != nil {
		t.Fatal(err)
	}
	if !recording.options.ReadOnly || recording.options.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) {
		t.Fatalf("authorization snapshot options=%+v", recording.options)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return tx.CheckBinding(t.Context(), p, capability.BindNone, capability.Target{Team: "a"})
	}); err != nil {
		t.Fatal(err)
	}
	if recording.options.ReadOnly || recording.locks != 2 {
		t.Fatalf("mutation did not lock both ancestors: options=%+v locks=%d", recording.options, recording.locks)
	}
}

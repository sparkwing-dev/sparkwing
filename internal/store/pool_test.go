package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

type idleConnector struct{}

func (idleConnector) Connect(context.Context) (driver.Conn, error) { return idleConn{}, nil }
func (idleConnector) Driver() driver.Driver                        { return nil }

type idleConn struct{}

func (idleConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (idleConn) Close() error                        { return nil }
func (idleConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }

func TestPostgresPoolKeepsEveryConnectionABurstOpened(t *testing.T) {
	db := sql.OpenDB(idleConnector{})
	t.Cleanup(func() { _ = db.Close() })
	configurePool(db, DialectPostgres)
	const burst = 25
	conns := make([]*sql.Conn, burst)
	for i := range conns {
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = conn
	}
	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if stats := db.Stats(); stats.MaxOpenConnections != burst || stats.Idle != burst || stats.MaxIdleClosed != 0 {
		t.Fatalf("after a burst of %d the pool kept %d idle and closed %d (open limit %d)", burst, stats.Idle, stats.MaxIdleClosed, stats.MaxOpenConnections)
	}
}

package store

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRollbackCleanupLogLevel(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	config, err := pgconn.ParseConfig("postgres://test@localhost/test?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgconn.Construct(&pgconn.HijackedConn{Conn: client, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Hijack(); err != nil {
		t.Fatal(err)
	}
	_, closed := conn.Exec(t.Context(), "ROLLBACK").ReadAll()
	_, uninitialized := new(pgconn.PgConn).Exec(t.Context(), "ROLLBACK").ReadAll()
	for _, tc := range []struct {
		name, level string
		err         error
	}{
		{"pgx closed", "DEBUG", closed},
		{"canceled", "DEBUG", context.Canceled},
		{"deadline", "DEBUG", context.DeadlineExceeded},
		{"sql closed", "DEBUG", sql.ErrConnDone},
		{"driver closed", "DEBUG", driver.ErrBadConn},
		{"network closed", "DEBUG", net.ErrClosed},
		{"server failure", "WARN", errors.New("rollback rejected")},
		{"untyped closed text", "WARN", errors.New("conn closed")},
		{"pgx uninitialized", "WARN", uninitialized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := fmt.Errorf("failed to deallocate cached statement(s): %w", tc.err)
			db := sql.OpenDB(&clockFailureConnector{rollbackErr: cause})
			defer db.Close()
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer slog.SetDefault(previous)
			rollbackOrLog(&storeTx{tx: tx})
			var record map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
				t.Fatal(err)
			}
			if record["level"] != tc.level || record["err"] != cause.Error() || record["msg"] != "rolling back a store transaction failed" {
				t.Fatalf("rollback log = %s, want %s with cause", &logs, tc.level)
			}
		})
	}
}

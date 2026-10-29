package store_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type standbyLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *standbyLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *standbyLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestControllerStandbyLogsBeforeWaiting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var logs standbyLog
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	server := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			server <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		backend := pgproto3.NewBackend(conn, conn)
		if _, err := backend.ReceiveStartupMessage(); err != nil {
			server <- err
			return
		}
		backend.Send(&pgproto3.AuthenticationOk{})
		backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
		backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		for {
			if err := backend.Flush(); err != nil {
				server <- err
				return
			}
			message, err := backend.Receive()
			if err != nil {
				server <- err
				return
			}
			query, ok := message.(*pgproto3.Query)
			if !ok {
				server <- fmt.Errorf("unexpected message %T", message)
				return
			}
			switch {
			case strings.Contains(query.String, "pg_advisory_lock("):
				text := logs.String()
				if strings.Count(text, "waiting for controller advisory lock") != 1 ||
					!strings.Contains(text, "lock_key=42") || !strings.Contains(text, "database=standby_test") ||
					!strings.Contains(text, "stop the active controller") || !strings.Contains(text, "cancel this startup") {
					server <- fmt.Errorf("standby wait lacks one actionable lock diagnostic: %q", text)
				} else {
					server <- nil
				}
				backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "57014", Message: "test ends lock wait"})
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'E'})
				_ = backend.Flush()
				return
			case strings.Contains(query.String, "hashtextextended"):
				backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("key"), DataTypeOID: 20, DataTypeSize: 8}}})
				backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("42")}})
			case strings.Contains(query.String, "pg_try_advisory_lock"):
				backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("held"), DataTypeOID: 16, DataTypeSize: 1}}})
				backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("f")}})
			}
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dsn := "postgres://test@" + listener.Addr().String() + "/standby_test?sslmode=disable&default_query_exec_mode=simple_protocol"
	st, err := store.OpenControllerPostgres(ctx, dsn, func(err error) { t.Error(err) })
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Error("expected the protocol fixture to end the lock wait")
	}
	if serverErr := <-server; serverErr != nil {
		t.Fatalf("protocol: %v; open: %v", serverErr, err)
	}
}

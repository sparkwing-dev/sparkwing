package store

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Connection names the actual transport endpoint, database and schema (or
// SQLite file), and the baseline timestamp a restored copy shares.
type Connection struct {
	Target  string `json:"target"`
	Created int64  `json:"created"`
}

const postgresStoreSchema = `COALESCE((SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass('sparkwing_schema_version')), current_schema())`

// Connection reads the identity on one retained session.
func (s *Store) Connection(ctx context.Context) (id Connection, err error) {
	conn := s.conn
	if conn == nil {
		conn, err = s.db.Conn(ctx)
		if err != nil {
			return Connection{}, err
		}
		defer func() { err = errors.Join(err, conn.Close()) }()
	}
	query := `SELECT 'sqlite:' || file, (SELECT applied_at FROM sparkwing_schema_version WHERE version = ?) FROM pragma_database_list WHERE name = 'main'`
	if s.dialect == DialectPostgres {
		query = `SELECT current_database() || '/' || ` + postgresStoreSchema + `, applied_at FROM sparkwing_schema_version WHERE version = ?`
	}
	if err := conn.QueryRowContext(ctx, rewritePh(s.dialect, query), expectedSchemaVersion).Scan(&id.Target, &id.Created); err != nil {
		return Connection{}, fmt.Errorf("read store identity: %w", err)
	}
	if s.dialect == DialectPostgres {
		if err := conn.Raw(func(raw any) error {
			endpoint := raw.(*stdlib.Conn).Conn().PgConn().Conn().RemoteAddr()
			id.Target = "postgres://" + endpoint.Network() + ":" + endpoint.String() + "/" + id.Target
			return nil
		}); err != nil {
			return Connection{}, err
		}
	}
	return id, nil
}

// PostgreSQL may bind a relative socket name. Retain the dialed directory,
// because getpeername alone cannot distinguish different Unix endpoints.
type postgresSocket struct {
	net.Conn
	endpoint *net.UnixAddr
}

func (c postgresSocket) RemoteAddr() net.Addr { return c.endpoint }

func postgresConfig(dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx parse errors retain the complete connection string.
		return nil, errors.New("invalid PostgreSQL connection settings")
	}
	dial := cfg.DialFunc
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err == nil && network == "unix" {
			return postgresSocket{conn, &net.UnixAddr{Net: network, Name: address}}, nil
		}
		return conn, err
	}
	return cfg, nil
}

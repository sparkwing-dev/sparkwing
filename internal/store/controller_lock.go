package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
)

// OpenControllerPostgres waits for the store's single-controller lock before
// migrations. failStop must terminate the process immediately, without draining.
// Promotion fences writes even before the former process detects session loss.
func OpenControllerPostgres(ctx context.Context, dsn string, failStop func(error), opts ...Option) (*Store, error) {
	cfg, err := postgresConfig(dsn)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	promotion, err := conn.Begin(ctx)
	if err != nil {
		return nil, errors.Join(err, conn.Close(context.WithoutCancel(ctx)))
	}
	var key int64
	err = conn.QueryRow(ctx, `SELECT hashtextextended('sparkwing.controller:' || current_database() || '/' || `+postgresStoreSchema+`, 0)`).Scan(&key)
	if err == nil {
		_, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, key)
	}
	if err != nil {
		return nil, errors.Join(err, conn.Close(context.WithoutCancel(ctx)))
	}
	// Pin reconnects to the endpoint whose session owns the lock.
	endpoint := conn.PgConn().Conn().RemoteAddr()
	dial := cfg.DialFunc
	cfg.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dial(ctx, endpoint.Network(), endpoint.String())
	}
	st, err := openPostgresConfig(ctx, cfg, opts...)
	if err == nil {
		// Row locks held by writes make promotion wait for their commits.
		err = promotion.QueryRow(ctx, `INSERT INTO settings(key,value) VALUES ('controller_epoch',convert_to('1','UTF8'))
ON CONFLICT(key) DO UPDATE SET value=convert_to((convert_from(settings.value,'UTF8')::bigint+1)::text,'UTF8')
RETURNING convert_from(value,'UTF8')::bigint`).Scan(&st.controllerEpoch)
	}
	if err == nil {
		err = promotion.Commit(ctx)
	}
	if err != nil {
		if st != nil {
			err = errors.Join(err, st.Close())
		}
		return nil, errors.Join(err, conn.Close(context.WithoutCancel(ctx)))
	}
	st.controllerFailStop = failStop
	watchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-tick.C:
				pingCtx, stop := context.WithTimeout(watchCtx, 5*time.Second)
				var held bool
				err := conn.QueryRow(pingCtx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND classid = $1::oid AND objid = $2::oid AND objsubid = 1 AND granted)`, uint32(uint64(key)>>32), uint32(key)).Scan(&held)
				stop()
				if watchCtx.Err() != nil {
					return
				}
				if err != nil || !held {
					failStop(fmt.Errorf("controller lock lost (held=%t): %v", held, err))
					return
				}
			}
		}
	}()
	cleanup := func() error {
		cancel()
		<-done
		return conn.Close(context.WithoutCancel(ctx))
	}
	closeConfig := st.cleanup
	st.cleanup = func() error { return errors.Join(closeConfig(), cleanup()) }
	return st, nil
}

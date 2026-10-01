package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPostgresLegacyAndReservationClaimsShareLockOrder(t *testing.T) {
	for _, method := range []string{"queue", "named"} {
		t.Run(method, func(t *testing.T) {
			s := storetest.NewPostgres(t).Open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			identity := enrollOfferExecutor(t, s, "worker", 100, 100)
			readyNode(t, s, "mixed", "ready")
			summary, err := s.SchedulingSummary(ctx, "mixed", "ready")
			if err != nil {
				t.Fatal(err)
			}
			blocker, err := s.DB().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback() }()
			if _, err := blocker.ExecContext(ctx, `SELECT node_id FROM nodes WHERE run_id = 'mixed' AND node_id = 'ready' FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			type result struct {
				node *store.Node
				err  error
			}
			done := make(chan result, 2)
			go func() {
				var n *store.Node
				var err error
				if method == "named" {
					n, err = s.ClaimNamedNode(ctx, identity, "mixed", "ready", "legacy", time.Minute, store.NamedClaimOptions{})
				} else {
					n, err = s.ClaimNextReadyNode(ctx, identity, "legacy", time.Minute, nil)
				}
				done <- result{n, err}
			}()
			waitBlocked := func(count int) {
				for {
					var blocked int
					if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity a
 WHERE a.wait_event_type = 'Lock' AND EXISTS (SELECT 1 FROM pg_locks l
 WHERE l.pid = a.pid AND l.relation = 'nodes'::regclass)`).Scan(&blocked); err != nil {
						t.Fatal(err)
					}
					if blocked >= count {
						return
					}
					select {
					case r := <-done:
						t.Fatalf("claim crossed the node row lock: %+v, %v", r.node, r.err)
					default:
					}
				}
			}
			waitBlocked(1)
			if _, err := blocker.ExecContext(ctx, `SELECT id FROM runs WHERE id = 'mixed' FOR NO KEY UPDATE NOWAIT`); err != nil {
				t.Fatalf("legacy claim holds the parent while waiting for its node: %v", err)
			}
			go func() {
				n, err := s.ClaimReadyNodeForExecutorWithReservation(ctx, identity, "worker", "mixed", "ready", "reservation", time.Minute, "reservation-id", 0, summary.ResourceDigest)
				done <- result{n, err}
			}()
			waitBlocked(2)
			if err := blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			awarded := 0
			for range 2 {
				r := <-done
				if r.node != nil && r.err == nil {
					awarded++
				} else if r.node != nil || !(errors.Is(r.err, store.ErrNotFound) || errors.Is(r.err, store.ErrLockHeld)) {
					t.Fatalf("mixed claim = %+v, %v", r.node, r.err)
				}
			}
			if awarded != 1 {
				t.Fatalf("mixed claims awarded %d nodes, want one", awarded)
			}
		})
	}
}

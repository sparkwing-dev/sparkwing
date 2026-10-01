package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPostgresNodeAwardRefusesParentFinishedAfterCandidateRead(t *testing.T) {
	for _, method := range []string{"queue", "named"} {
		for _, status := range []string{"failed", "success", "cancelled"} {
			t.Run(method+"/"+status, func(t *testing.T) {
				s := storetest.NewPostgres(t).Open(t)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				claimant := meteredClaimant(t, s, "cloud")
				if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent, "funds", "admin"); err != nil {
					t.Fatal(err)
				}
				readyNode(t, s, "racing", "ready")
				blocker, err := s.DB().BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = blocker.Rollback() }()
				var blockerPID int
				if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM runs WHERE id = 'racing' FOR NO KEY UPDATE`).Scan(&blockerPID); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					var n *store.Node
					var err error
					if method == "named" {
						n, err = s.ClaimNamedNode(ctx, claimant, "racing", "ready", "holder", time.Minute, store.NamedClaimOptions{})
					} else {
						n, err = s.ClaimNextReadyNode(ctx, claimant, "holder", time.Minute, nil)
					}
					if n != nil || !(errors.Is(err, store.ErrLockHeld) || errors.Is(err, store.ErrNotFound)) {
						done <- errors.New("claim awarded or failed for a reason other than terminal parent")
						return
					}
					done <- nil
				}()
				for {
					var blocked bool
					if err := s.DB().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&blocked); err != nil {
						t.Fatal(err)
					}
					if blocked {
						break
					}
					select {
					case err := <-done:
						t.Fatalf("award crossed the parent row lock: %v", err)
					default:
					}
				}
				if _, err := blocker.ExecContext(ctx, `UPDATE runs SET status = $1, finished_at = $2 WHERE id = 'racing'`, status, time.Now().UnixNano()); err != nil {
					t.Fatal(err)
				}
				if err := blocker.Commit(); err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if charges, err := s.ListCreditCharges(ctx, 10); err != nil || len(charges) != 0 {
					t.Fatalf("terminal credit charges = %v, %v", charges, err)
				}
			})
		}
	}
}

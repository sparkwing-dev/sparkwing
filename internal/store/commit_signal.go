package store

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type commitSignal struct {
	mu   sync.Mutex
	next chan struct{}
}

func (c *commitSignal) wait() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.next == nil {
		c.next = make(chan struct{})
	}
	return c.next
}

func (c *commitSignal) fire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.next != nil {
		close(c.next)
		c.next = nil
	}
}

// NextCommit returns a channel closed when a transaction that wrote commits after this call.
// It sees only this process's writes.
func (s *Store) NextCommit() <-chan struct{} {
	return s.commits.wait()
}

// NextGroupWaitDeadline reports how long until the earliest concurrency-group
// queue timeout of a team's waiting job lapses; ok is false when no job is timing one.
func (s *Store) NextGroupWaitDeadline(ctx context.Context, team Team) (wait time.Duration, ok bool, err error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { err = errors.Join(err, tx.Rollback()) }()
	// perf: the predicate matches idx_nodes_claim_window, so only ready unclaimed jobs are visited.
	rows, err := tx.queryContext(ctx, `SELECT run_id,node_id FROM nodes WHERE team=? AND status='pending' AND ready_at IS NOT NULL AND claimed_by IS NULL AND capability_prefix='' AND cancel_requested_at IS NULL AND group_wait_started_at IS NOT NULL`, team)
	if err != nil {
		return 0, false, err
	}
	var jobs []runspec.NodeRef
	for rows.Next() {
		var job runspec.NodeRef
		if err := rows.Scan(&job.RunID, &job.NodeID); err != nil {
			return 0, false, errors.Join(err, rows.Close())
		}
		jobs = append(jobs, job)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, false, err
	}
	var earliest time.Time
	for _, job := range jobs {
		row, err := readNodeTx(ctx, tx, team, job)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, false, err
		}
		if row.GroupWaitStartedAt == nil {
			continue
		}
		for _, group := range row.Execution.Concurrency {
			if group.QueueTimeoutMS == nil || *group.QueueTimeoutMS <= 0 {
				continue
			}
			lapse := row.GroupWaitStartedAt.Add(time.Duration(*group.QueueTimeoutMS) * time.Millisecond)
			if earliest.IsZero() || lapse.Before(earliest) {
				earliest = lapse
			}
		}
	}
	if earliest.IsZero() {
		return 0, false, nil
	}
	return max(earliest.Sub(tx.now(ctx)), 0), true, nil
}

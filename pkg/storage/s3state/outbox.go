package s3state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// DefaultOutboxDrainInterval bounds how often the drainer polls the
// outbox for queued writes when the object store is reachable again.
const DefaultOutboxDrainInterval = 5 * time.Second

// OutboxKind tags a queued write so the drainer can route it to the
// matching store on replay.
type OutboxKind string

const (
	OutboxKindState    OutboxKind = "state"
	OutboxKindArtifact OutboxKind = "artifact"
	OutboxKindLog      OutboxKind = "log"
)

// Outbox persists writes that failed transiently against the object
// store and drains them in FIFO order when connectivity returns.
// Backed by a single-file SQLite database; safe for one writer per
// process.
type Outbox struct {
	db       *sql.DB
	art      storage.ArtifactStore
	interval time.Duration

	mu       sync.Mutex
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// OpenOutbox initializes the outbox at path. The artifact store
// receives drained writes once they're re-runnable. Pass interval=0
// for the default poll cadence.
func OpenOutbox(path string, art storage.ArtifactStore, interval time.Duration) (*Outbox, error) {
	if art == nil {
		return nil, errors.New("s3state: OpenOutbox requires an artifact store")
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("s3state: open outbox: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS outbox_writes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT NOT NULL,
    key         TEXT NOT NULL,
    body        BLOB NOT NULL,
    enqueued_at INTEGER NOT NULL
);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("s3state: outbox migrate: %w", err)
	}
	o := &Outbox{
		db:       db,
		art:      art,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
	if o.interval <= 0 {
		o.interval = DefaultOutboxDrainInterval
	}
	o.wg.Add(1)
	go o.drainLoop()
	return o, nil
}

// Stage enqueues a write. Idempotent only at the byte-identical level
// (replay re-PUTs whatever bytes are in the row, so re-issuing a
// state PUT is harmless because the contents include all prior
// envelopes).
func (o *Outbox) Stage(ctx context.Context, kind OutboxKind, key string, body []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, err := o.db.ExecContext(ctx, `
INSERT INTO outbox_writes (kind, key, body, enqueued_at)
VALUES (?, ?, ?, ?)`,
		string(kind), key, body, time.Now().UnixNano())
	return err
}

// Pending returns the count of queued writes. Test helper.
func (o *Outbox) Pending(ctx context.Context) (int, error) {
	row := o.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_writes`)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Drain attempts to replay every queued write in id order. Stops on
// the first PUT that fails with a transient error so the FIFO
// invariant survives across retries. Non-transient errors delete the
// offending row (the user already saw the original 4xx) and continue.
func (o *Outbox) Drain(ctx context.Context) error {
	for {
		o.mu.Lock()
		row := o.db.QueryRowContext(ctx, `
SELECT id, kind, key, body FROM outbox_writes ORDER BY id ASC LIMIT 1`)
		var id int64
		var kind, key string
		var body []byte
		err := row.Scan(&id, &kind, &key, &body)
		o.mu.Unlock()
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		switch OutboxKind(kind) {
		case OutboxKindState, OutboxKindArtifact:
			perr := o.art.Put(ctx, key, byteReader(body))
			if perr != nil {
				if isTransient(perr) {
					return perr
				}
				// Non-transient: drop and surface to logs via the next
				// caller's error handling. We delete so the queue doesn't
				// jam on a permanent failure.
			}
		case OutboxKindLog:
			// Log replay is out of scope here -- the log backend has
			// its own outbox if needed. Drop the row.
		default:
			// Unknown kind from a forward-incompatible writer; drop.
		}
		o.mu.Lock()
		_, _ = o.db.ExecContext(ctx, `DELETE FROM outbox_writes WHERE id = ?`, id)
		o.mu.Unlock()
	}
}

func (o *Outbox) drainLoop() {
	defer o.wg.Done()
	t := time.NewTicker(o.interval)
	defer t.Stop()
	for {
		select {
		case <-o.stopCh:
			return
		case <-t.C:
			_ = o.Drain(context.Background())
		}
	}
}

// Close stops the background drainer. Queued rows remain on disk and
// resume draining on the next OpenOutbox.
func (o *Outbox) Close() error {
	o.stopOnce.Do(func() { close(o.stopCh) })
	o.wg.Wait()
	return o.db.Close()
}

// byteReader wraps body in a fresh reader on every call (Put may be
// retried under the hood by the AWS SDK).
type byteReaderImpl struct{ b []byte }

func (r *byteReaderImpl) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

func byteReader(b []byte) *byteReaderImpl { return &byteReaderImpl{b: b} }

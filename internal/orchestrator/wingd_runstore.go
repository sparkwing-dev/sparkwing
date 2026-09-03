package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// HeldRunStoreRetry is how long a failed open of a store that exists is
// cached before another attempt. An absent store is retried on every call,
// because the attempt is a stat.
const HeldRunStoreRetry = 30 * time.Second

// FinalizeTimeout bounds one finalize against the runs store. It is the
// first window in the shutdown chain described on
// [github.com/sparkwing-dev/sparkwing/internal/wingd.FinalizeDrainWindow].
const FinalizeTimeout = 8 * time.Second

const (
	terminalCheckTimeout = 5 * time.Second
	readyOpenBudget      = 250 * time.Millisecond
)

var errRunStoreClosed = errors.New("the runs store handle is closed")

var errRunStoreUnopened = errors.New("the runs store is not open yet")

var errRunStoreAbsent = errors.New("the runs store does not exist yet")

var errRunStoreReplaced = errors.New("the runs store was replaced")

// HeldRunStore is the daemon host's handle on the runs store: opened once,
// held for the daemon's lifetime, and closed when the daemon exits.
//
// It holds two handles on the same file. A SQLite store is one connection,
// so the writing handle that serves finalize and the reaper serializes every
// caller behind whatever it is doing; the terminal check on the admission
// path therefore reads through a separate read-only handle, which under WAL
// never waits on a writer.
//
// Opening is done by one goroutine, and callers wait for it under their own
// context. Nothing here blocks a caller past its deadline: a store that will
// not open, or one whose migration waits out another process's write lock,
// evicts a run with that reason instead of stalling admission for the
// machine.
//
// An unusable store never stops the daemon serving. Open failures are
// reported by Ready and returned to the terminal check, the open is retried,
// and a store file that is replaced or removed is noticed and reopened.
type HeldRunStore struct {
	paths Paths
	retry time.Duration
	now   func() time.Time

	mu        sync.Mutex
	rw        *store.Store
	ro        *store.Store
	info      os.FileInfo
	err       error
	attempted time.Time
	opening   chan struct{}
	openedAt  time.Time
	closed    bool
}

// NewHeldRunStore resolves home's runs store without opening it. Open
// happens on the first call that needs it.
func NewHeldRunStore(home string) (*HeldRunStore, error) {
	p := PathsAt(home)
	if home == "" {
		var err error
		p, err = DefaultPaths()
		if err != nil {
			return nil, err
		}
	}
	return &HeldRunStore{paths: p, retry: HeldRunStoreRetry, now: time.Now}, nil
}

// Store returns the writing handle, opening the store when it is closed and
// either force is set or the retry interval has passed. It waits for an open
// in flight only as long as ctx allows. Reads on the admission path belong on
// Reader instead.
func (h *HeldRunStore) Store(ctx context.Context, force bool) (*store.Store, error) {
	rw, _, err := h.handles(ctx, force)
	return rw, err
}

// Reader returns the read-only handle, opened alongside the writing one,
// waiting for an open in flight only as long as ctx allows.
func (h *HeldRunStore) Reader(ctx context.Context, force bool) (*store.Store, error) {
	_, ro, err := h.handles(ctx, force)
	return ro, err
}

func (h *HeldRunStore) handles(ctx context.Context, force bool) (*store.Store, *store.Store, error) {
	h.revalidate()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, nil, errRunStoreClosed
	}
	if h.rw != nil {
		rw, ro := h.rw, h.ro
		h.mu.Unlock()
		return rw, ro, nil
	}
	if !force && h.cachedFailureLocked() {
		err := h.err
		h.mu.Unlock()
		return nil, nil, err
	}
	done := h.beginOpenLocked()
	started, pending := h.openedAt, h.err
	h.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return nil, nil, stillOpening(h.now().Sub(started), pending, ctx.Err())
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.closed:
		return nil, nil, errRunStoreClosed
	case h.rw != nil:
		return h.rw, h.ro, nil
	default:
		return nil, nil, h.err
	}
}

// safety: store.Open migrates, and a migration waits out another process's
// write lock with no deadline of its own, so the open runs on one goroutine
// that holds no lock while it blocks and every caller waits on this channel
// under its own deadline instead.
func (h *HeldRunStore) beginOpenLocked() chan struct{} {
	if h.opening != nil {
		return h.opening
	}
	done := make(chan struct{})
	h.opening = done
	h.openedAt = h.now()
	go func() {
		defer close(done)
		rw, ro, info, err := h.openStore()
		h.mu.Lock()
		defer h.mu.Unlock()
		h.opening = nil
		if h.closed {
			closeHandles(rw, ro)
			return
		}
		h.rw, h.ro, h.info, h.err, h.attempted = rw, ro, info, err, h.now()
	}()
	return done
}

func stillOpening(elapsed time.Duration, pending, cause error) error {
	if pending != nil && !errors.Is(pending, errRunStoreAbsent) {
		return fmt.Errorf("the runs store is still opening (%s so far, last failure: %w): %w",
			elapsed.Round(100*time.Millisecond), pending, cause)
	}
	return fmt.Errorf("the runs store is still opening (%s so far): %w",
		elapsed.Round(100*time.Millisecond), cause)
}

// safety: an absent store costs a stat to retry, so only a failed open of a
// file that exists is worth caching; caching absence stalls the first run on
// a fresh home behind the retry interval.
func (h *HeldRunStore) cachedFailureLocked() bool {
	if h.attempted.IsZero() || errors.Is(h.err, errRunStoreAbsent) {
		return false
	}
	return h.now().Sub(h.attempted) < h.retry
}

// safety: a run against an object-store profile keeps no local state, so the
// daemon opens the file it finds and never creates one.
func (h *HeldRunStore) openStore() (*store.Store, *store.Store, os.FileInfo, error) {
	db := h.paths.StateDB()
	info, err := os.Stat(db)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil, errRunStoreAbsent
		}
		return nil, nil, nil, err
	}
	rw, err := store.Open(db)
	if err != nil {
		return nil, nil, nil, err
	}
	// safety: the read-only open neither migrates nor checks requirements, so
	// it follows the writing open, which refuses a store this binary cannot
	// understand.
	ro, err := store.OpenReadOnly(db)
	if err != nil {
		_ = rw.Close()
		return nil, nil, nil, err
	}
	return rw, ro, info, nil
}

// safety: an open handle proves only that the file opened once. A store that
// was deleted or replaced leaves the daemon reading an inode nobody else can
// see, so every readiness check and every pass compares the file identity.
func (h *HeldRunStore) revalidate() {
	h.mu.Lock()
	rw, ro, info, closed := h.rw, h.ro, h.info, h.closed
	if closed || rw == nil {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	current, err := os.Stat(h.paths.StateDB())
	if err == nil && os.SameFile(current, info) {
		return
	}
	reason := errRunStoreReplaced
	if err != nil {
		reason = err
		if errors.Is(err, os.ErrNotExist) {
			reason = errRunStoreAbsent
		}
	}
	h.mu.Lock()
	if h.closed || h.rw != rw {
		h.mu.Unlock()
		return
	}
	h.rw, h.ro, h.info, h.err, h.attempted = nil, nil, nil, reason, time.Time{}
	h.mu.Unlock()
	go closeHandles(rw, ro)
}

func closeHandles(rw, ro *store.Store) {
	if ro != nil {
		_ = ro.Close()
	}
	if rw != nil {
		_ = rw.Close()
	}
}

// safety: revalidate closes a replaced handle while a caller may still hold
// the pointer it was given, and database/sql reports that only as a message,
// so a closed handle costs one retry against the reopened store rather than
// an eviction.
func closedHandle(err error) bool {
	return err != nil && strings.Contains(err.Error(), "database is closed")
}

// Ready returns why the handle is unusable, or nil when it is open on the
// store file that is there now. It waits at most [readyOpenBudget] for an
// open in flight, so a handshake never waits on the store beyond that.
func (h *HeldRunStore) Ready() error {
	ctx, cancel := context.WithTimeout(context.Background(), readyOpenBudget)
	defer cancel()
	if _, _, err := h.handles(ctx, false); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return h.state()
	}
	return nil
}

func (h *HeldRunStore) state() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.closed:
		return errRunStoreClosed
	case h.rw != nil:
		return nil
	case h.err != nil:
		return h.err
	default:
		return errRunStoreUnopened
	}
}

// Close releases both handles. Later calls report a closed store rather than
// reopening it.
func (h *HeldRunStore) Close() error {
	h.mu.Lock()
	rw, ro := h.rw, h.ro
	h.rw, h.ro, h.info, h.closed = nil, nil, nil, true
	h.mu.Unlock()
	var err error
	if ro != nil {
		err = ro.Close()
	}
	if rw != nil {
		if rwErr := rw.Close(); err == nil {
			err = rwErr
		}
	}
	return err
}

func (h *HeldRunStore) IsRunTerminal(runID string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), terminalCheckTimeout)
	defer cancel()
	terminal := false
	err := h.read(ctx, func(st *store.Store) error {
		run, err := st.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		terminal = isTerminalStatus(run.Status)
		return nil
	})
	if errors.Is(err, errRunStoreAbsent) || errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, stalled(err, terminalCheckTimeout)
	}
	return terminal, nil
}

func (h *HeldRunStore) FinalizeRun(runID string) {
	const reason = "interrupted: run process exited without finalizing (admission connection lost)"
	if err := h.finalizeRun(runID, reason); err != nil {
		slog.Warn("wingd: finalize orphaned run", "run_id", runID, "err", err)
	}
}

func (h *HeldRunStore) finalizeRun(runID, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), FinalizeTimeout)
	defer cancel()
	err := h.write(ctx, func(st *store.Store) error {
		run, err := st.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if isTerminalStatus(run.Status) {
			return nil
		}
		return st.FinishRun(ctx, runID, "cancelled", reason)
	})
	if errors.Is(err, errRunStoreAbsent) || errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return stalled(err, FinalizeTimeout)
}

func (h *HeldRunStore) FinalizeCancelledRuns(runIDs []string, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), FinalizeTimeout)
	defer cancel()
	err := h.write(ctx, func(st *store.Store) error {
		return st.FinishRunsIfActive(ctx, runIDs, "cancelled", reason)
	})
	if errors.Is(err, errRunStoreAbsent) {
		return nil
	}
	return stalled(err, FinalizeTimeout)
}

func (h *HeldRunStore) read(ctx context.Context, fn func(*store.Store) error) error {
	return h.call(ctx, fn, h.Reader)
}

func (h *HeldRunStore) write(ctx context.Context, fn func(*store.Store) error) error {
	return h.call(ctx, fn, h.Store)
}

func (h *HeldRunStore) call(ctx context.Context, fn func(*store.Store) error, handle func(context.Context, bool) (*store.Store, error)) error {
	for attempt := 0; ; attempt++ {
		st, err := handle(ctx, true)
		if err != nil {
			return err
		}
		err = fn(st)
		if attempt == 0 && closedHandle(err) {
			continue
		}
		return err
	}
}

func stalled(err error, within time.Duration) error {
	if err != nil && errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "still opening") {
		return fmt.Errorf("the runs store did not answer within %s: %w", within, err)
	}
	return err
}

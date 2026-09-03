package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// HeldRunStoreRetry is how long a failed open of a store that exists is
// cached before another attempt. An absent store is retried on every call,
// because the attempt is a stat.
const HeldRunStoreRetry = 30 * time.Second

const (
	terminalCheckTimeout = 5 * time.Second
	finalizeTimeout      = 30 * time.Second
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
// never waits on a writer. Every call carries a deadline, so a store that
// stops answering evicts a run with that reason instead of hanging admission
// for the machine.
//
// An unusable store never stops the daemon serving. Open failures are
// reported by Ready and returned to the terminal check, the open is retried,
// and a store file that is replaced or removed is noticed and reopened.
type HeldRunStore struct {
	paths Paths
	retry time.Duration
	now   func() time.Time

	openMu sync.Mutex

	mu        sync.RWMutex
	rw        *store.Store
	ro        *store.Store
	info      os.FileInfo
	err       error
	attempted time.Time
	opening   chan struct{}
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
// either force is set or the retry interval has passed. Reads on the
// admission path belong on Reader instead.
func (h *HeldRunStore) Store(force bool) (*store.Store, error) {
	rw, _, err := h.handles(force)
	return rw, err
}

// Reader returns the read-only handle, opened alongside the writing one.
func (h *HeldRunStore) Reader(force bool) (*store.Store, error) {
	_, ro, err := h.handles(force)
	return ro, err
}

func (h *HeldRunStore) handles(force bool) (*store.Store, *store.Store, error) {
	h.revalidate()
	h.openMu.Lock()
	defer h.openMu.Unlock()
	h.mu.RLock()
	rw, ro, lastErr, attempted, closed := h.rw, h.ro, h.err, h.attempted, h.closed
	h.mu.RUnlock()
	if closed {
		return nil, nil, errRunStoreClosed
	}
	if rw != nil {
		return rw, ro, nil
	}
	if !force && h.cachedFailure(lastErr, attempted) {
		return nil, nil, lastErr
	}
	newRW, newRO, info, openErr := h.openStore()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		closeHandles(newRW, newRO)
		return nil, nil, errRunStoreClosed
	}
	h.rw, h.ro, h.info, h.err, h.attempted = newRW, newRO, info, openErr, h.now()
	h.mu.Unlock()
	return newRW, newRO, openErr
}

// safety: an absent store costs a stat to retry, so only a failed open of a
// file that exists is worth caching; caching absence stalls the first run on
// a fresh home behind the retry interval.
func (h *HeldRunStore) cachedFailure(lastErr error, attempted time.Time) bool {
	if attempted.IsZero() || errors.Is(lastErr, errRunStoreAbsent) {
		return false
	}
	return h.now().Sub(attempted) < h.retry
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
	h.mu.RLock()
	rw, ro, info, closed := h.rw, h.ro, h.info, h.closed
	h.mu.RUnlock()
	if closed || rw == nil {
		return
	}
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

// Ready returns why the handle is unusable, or nil when it is open on the
// store file that is there now. It bounds the work it does, so a handshake
// never waits on the store beyond that budget.
func (h *HeldRunStore) Ready() error {
	h.revalidate()
	if err := h.state(); err == nil {
		return nil
	}
	h.openWithinBudget()
	return h.state()
}

func (h *HeldRunStore) state() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
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

func (h *HeldRunStore) openWithinBudget() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	done := h.opening
	if done == nil {
		done = make(chan struct{})
		h.opening = done
		go func() {
			_, _, _ = h.handles(false)
			h.mu.Lock()
			h.opening = nil
			h.mu.Unlock()
			close(done)
		}()
	}
	h.mu.Unlock()
	timer := time.NewTimer(readyOpenBudget)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
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
	st, err := h.Reader(true)
	if errors.Is(err, errRunStoreAbsent) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), terminalCheckTimeout)
	defer cancel()
	run, err := st.GetRun(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, storeStalled(err, terminalCheckTimeout)
	}
	return isTerminalStatus(run.Status), nil
}

func (h *HeldRunStore) FinalizeRun(runID string) {
	const reason = "interrupted: run process exited without finalizing (admission connection lost)"
	if err := h.finalizeRun(runID, reason); err != nil {
		slog.Warn("wingd: finalize orphaned run", "run_id", runID, "err", err)
	}
}

func (h *HeldRunStore) finalizeRun(runID, reason string) error {
	st, err := h.Store(true)
	if errors.Is(err, errRunStoreAbsent) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return storeStalled(err, finalizeTimeout)
	}
	if isTerminalStatus(run.Status) {
		return nil
	}
	return storeStalled(st.FinishRun(ctx, runID, "cancelled", reason), finalizeTimeout)
}

func (h *HeldRunStore) FinalizeCancelledRuns(runIDs []string, reason string) error {
	st, err := h.Store(true)
	if errors.Is(err, errRunStoreAbsent) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	return storeStalled(st.FinishRunsIfActive(ctx, runIDs, "cancelled", reason), finalizeTimeout)
}

func storeStalled(err error, within time.Duration) error {
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("the runs store did not answer within %s: %w", within, err)
	}
	return err
}

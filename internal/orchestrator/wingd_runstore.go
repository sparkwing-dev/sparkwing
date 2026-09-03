package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// HeldRunStoreRetry is how long a failed open is cached before a background
// pass tries again. A store-backed call retries immediately.
const HeldRunStoreRetry = 30 * time.Second

var errRunStoreClosed = errors.New("the runs store handle is closed")

var errRunStoreUnopened = errors.New("the runs store is not open yet")

var errRunStoreAbsent = errors.New("the runs store does not exist yet")

// HeldRunStore is the daemon host's handle on the runs store: opened once,
// held for the daemon's lifetime, and closed when the daemon exits. It
// serves the daemon's terminal check and finalize callbacks, and the
// maintenance loop reaps through the same handle.
//
// An unusable store never stops the daemon. Open failures are reported by
// Ready and returned to the terminal check, which evicts the run with that
// reason, and a later open succeeds without a restart.
type HeldRunStore struct {
	paths Paths
	retry time.Duration
	now   func() time.Time

	openMu sync.Mutex

	mu        sync.RWMutex
	st        *store.Store
	err       error
	attempted time.Time
	closed    bool
}

// NewHeldRunStore resolves home's runs store without opening it. Open
// happens on the first Store call or maintenance pass.
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

// Store returns the open handle, opening it when it is closed and either
// force is set or the retry interval has passed since the last attempt.
func (h *HeldRunStore) Store(force bool) (*store.Store, error) {
	h.openMu.Lock()
	defer h.openMu.Unlock()
	h.mu.RLock()
	st, lastErr, attempted, closed := h.st, h.err, h.attempted, h.closed
	h.mu.RUnlock()
	if closed {
		return nil, errRunStoreClosed
	}
	if st != nil {
		return st, nil
	}
	if !force && !attempted.IsZero() && h.now().Sub(attempted) < h.retry {
		return nil, lastErr
	}
	opened, openErr := h.openStore()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		if opened != nil {
			_ = opened.Close()
		}
		return nil, errRunStoreClosed
	}
	h.st, h.err, h.attempted = opened, openErr, h.now()
	h.mu.Unlock()
	return opened, openErr
}

// safety: a run against an object-store profile keeps no local state, so the
// daemon opens the file it finds and never creates one.
func (h *HeldRunStore) openStore() (*store.Store, error) {
	if _, err := os.Stat(h.paths.StateDB()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errRunStoreAbsent
		}
		return nil, err
	}
	return store.Open(h.paths.StateDB())
}

// Ready reports why the handle is unusable, or nil when it is open. It reads
// the last open attempt rather than making one, so a handshake never waits on
// the store.
func (h *HeldRunStore) Ready() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	switch {
	case h.closed:
		return errRunStoreClosed
	case h.st != nil:
		return nil
	case h.err != nil:
		return h.err
	default:
		return errRunStoreUnopened
	}
}

// Close releases the handle. Later calls report a closed store rather than
// reopening it.
func (h *HeldRunStore) Close() error {
	h.mu.Lock()
	st := h.st
	h.st, h.closed = nil, true
	h.mu.Unlock()
	if st == nil {
		return nil
	}
	return st.Close()
}

func (h *HeldRunStore) IsRunTerminal(runID string) (bool, error) {
	st, err := h.Store(true)
	if errors.Is(err, errRunStoreAbsent) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	run, err := st.GetRun(context.Background(), runID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
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
	ctx := context.Background()
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if isTerminalStatus(run.Status) {
		return nil
	}
	return st.FinishRun(ctx, runID, "cancelled", reason)
}

func (h *HeldRunStore) FinalizeCancelledRuns(runIDs []string, reason string) error {
	st, err := h.Store(true)
	if errors.Is(err, errRunStoreAbsent) {
		return nil
	}
	if err != nil {
		return err
	}
	return st.FinishRunsIfActive(context.Background(), runIDs, "cancelled", reason)
}
